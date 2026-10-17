package handlers

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What may be attached to a chat message, decided from the BYTES (never from the
// Content-Type or the extension the browser sent). A closed list: anything not
// named here is refused. See docs/superpowers/specs/2026-10-17-chat-interno-arquivos-e-audio-design.md.

const (
	chatMaxFileBytes   = 10 << 20 // D1: per file
	chatMaxAudioBytes  = 5 << 20
	chatMaxAttachments = 5 // per message
	chatMaxPending     = 10
	chatMaxPixels      = 8000 // per side
	chatMaxZipEntries  = 200
	chatMaxZipBytes    = 50 << 20
	chatMaxAudioMillis = 300_000
	chatMaxFileNameRunes   = 255
)

var (
	errFileType    = errors.New("file type not allowed")
	errFileInvalid = errors.New("invalid file")
)

// ChatFileInfo is what inspection learned about a file.
type ChatFileInfo struct {
	Kind          string // image | audio | file
	Mime          string
	Ext           string // from the detected type, with the dot
	Width, Height int
}

// chatTypes maps a detected type to its kind, the stored extension and the
// extensions a sender may legitimately have used for it.
var chatTypes = map[string]struct {
	kind string
	ext  string
	alts []string
}{
	"image/png":       {"image", ".png", []string{"png"}},
	"image/jpeg":      {"image", ".jpg", []string{"jpg", "jpeg", "jpe", "jfif"}},
	"image/gif":       {"image", ".gif", []string{"gif"}},
	"image/webp":      {"image", ".webp", []string{"webp"}},
	"application/pdf": {"file", ".pdf", []string{"pdf"}},
	"text/plain":      {"file", ".txt", []string{"txt"}},
	"text/csv":        {"file", ".csv", []string{"csv"}},
	"audio/webm":      {"audio", ".webm", []string{"webm", "weba"}},
	"audio/mp4":       {"audio", ".m4a", []string{"m4a", "mp4", "aac"}},
	"audio/ogg":       {"audio", ".ogg", []string{"ogg", "oga", "opus"}},
	"audio/mpeg":      {"audio", ".mp3", []string{"mp3"}},
	"audio/wav":       {"audio", ".wav", []string{"wav"}},
	docxMime:          {"file", ".docx", []string{"docx"}},
	xlsxMime:          {"file", ".xlsx", []string{"xlsx"}},
	pptxMime:          {"file", ".pptx", []string{"pptx"}},
}

const (
	docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	xlsxMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	pptxMime = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// dangerousExt: if ANY dot-separated part of the name is one of these ("a.pdf.exe"),
// the file is refused whatever its bytes look like.
var dangerousExt = map[string]bool{
	"exe": true, "bat": true, "cmd": true, "com": true, "scr": true, "pif": true, "msi": true, "dll": true,
	"js": true, "mjs": true, "jse": true, "vbs": true, "vbe": true, "wsf": true, "ps1": true, "sh": true,
	"jar": true, "apk": true, "app": true, "lnk": true, "reg": true, "hta": true, "cpl": true,
	"html": true, "htm": true, "xhtml": true, "svg": true, "svgz": true, "xml": true, "php": true, "asp": true,
	"aspx": true, "jsp": true, "docm": true, "xlsm": true, "pptm": true,
}

// sanitizeChatFileName makes a display label out of whatever the client sent: no
// path, no control or direction-override characters, no leading dots, bounded.
func sanitizeChatFileName(raw, fallbackExt string) string {
	name := raw
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), r == 0x2028, r == 0x2029, r == 0xfeff,
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f:
			continue
		}
		b.WriteRune(r)
	}
	name = strings.Join(strings.Fields(b.String()), " ")
	name = strings.TrimLeft(name, ". ")
	if r := []rune(name); len(r) > chatMaxFileNameRunes {
		name = string(r[:chatMaxFileNameRunes])
	}
	if name == "" {
		name = "arquivo" + fallbackExt
	}
	return name
}

func hasDangerousExt(name string) bool {
	parts := strings.Split(strings.ToLower(name), ".")
	for _, p := range parts[1:] {
		if dangerousExt[strings.TrimSpace(p)] {
			return true
		}
	}
	return false
}

// inspectChatFile decides what the bytes are. wantAudio is the sender's claim that
// this is a recording: WebM and MP4 are ambiguous containers (they may hold video),
// so they are accepted only as audio when claimed as such.
func inspectChatFile(data []byte, rawName string, wantAudio bool) (ChatFileInfo, error) {
	if len(data) == 0 {
		return ChatFileInfo{}, errFileInvalid
	}
	if hasDangerousExt(rawName) {
		return ChatFileInfo{}, errFileType
	}
	mime, err := detectChatMime(data, rawName, wantAudio)
	if err != nil {
		return ChatFileInfo{}, err
	}
	t := chatTypes[mime]

	// The extension the sender used must belong to the detected type.
	if ext := strings.ToLower(strings.TrimPrefix(path.Ext(rawName), ".")); ext != "" {
		ok := false
		for _, a := range t.alts {
			if a == ext {
				ok = true
			}
		}
		if !ok {
			return ChatFileInfo{}, errFileInvalid
		}
	}

	info := ChatFileInfo{Kind: t.kind, Mime: mime, Ext: t.ext}
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return ChatFileInfo{}, errFileInvalid
		}
		info.Width, info.Height = cfg.Width, cfg.Height
	case "image/webp":
		w, h, ok := webpSize(data)
		if !ok {
			return ChatFileInfo{}, errFileInvalid
		}
		info.Width, info.Height = w, h
	}
	if info.Kind == "image" && (info.Width < 1 || info.Height < 1 || info.Width > chatMaxPixels || info.Height > chatMaxPixels) {
		return ChatFileInfo{}, errFileInvalid
	}
	if info.Kind == "audio" && len(data) > chatMaxAudioBytes {
		return ChatFileInfo{}, errFileInvalid
	}
	return info, nil
}

func detectChatMime(data []byte, rawName string, wantAudio bool) (string, error) {
	has := func(prefix string) bool { return bytes.HasPrefix(data, []byte(prefix)) }
	switch {
	case has("\x89PNG\r\n\x1a\n"):
		return "image/png", nil
	case has("\xff\xd8\xff"):
		return "image/jpeg", nil
	case has("GIF87a"), has("GIF89a"):
		return "image/gif", nil
	case len(data) >= 12 && has("RIFF") && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	case len(data) >= 12 && has("RIFF") && string(data[8:12]) == "WAVE":
		return "audio/wav", nil
	case has("%PDF-"):
		return "application/pdf", nil
	case has("OggS"):
		return "audio/ogg", nil
	case has("ID3"), len(data) > 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
		return "audio/mpeg", nil
	case has("\x1a\x45\xdf\xa3"):
		head := data
		if len(head) > 64 {
			head = head[:64]
		}
		if wantAudio && bytes.Contains(head, []byte("webm")) {
			return "audio/webm", nil
		}
		return "", errFileType
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		if wantAudio {
			return "audio/mp4", nil
		}
		return "", errFileType
	case has("PK\x03\x04"):
		return detectOOXML(data)
	}
	// Plain text and CSV: valid UTF-8, no NUL, and a matching extension.
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(rawName), "."))
	if ext == "txt" || ext == "csv" {
		sample := data
		truncated := len(sample) > 1<<20
		if truncated {
			sample = sample[:1<<20]
		}
		if bytes.IndexByte(sample, 0) >= 0 {
			return "", errFileInvalid
		}
		// A cut in the middle of a character is fine; anything else invalid is not.
		for i := 0; truncated && i < 3 && !utf8.Valid(sample); i++ {
			sample = sample[:len(sample)-1]
		}
		if !utf8.Valid(sample) {
			return "", errFileInvalid
		}
		if ext == "csv" {
			return "text/csv", nil
		}
		return "text/plain", nil
	}
	return "", errFileType
}

// detectOOXML recognises docx/xlsx/pptx without extracting anything and refuses
// macro-enabled files and archives that look like zip bombs.
func detectOOXML(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errFileType
	}
	if len(zr.File) > chatMaxZipEntries {
		return "", errFileInvalid
	}
	var total uint64
	var hasTypes bool
	family := ""
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > chatMaxZipBytes {
			return "", errFileInvalid
		}
		name := strings.ToLower(f.Name)
		switch {
		case name == "[content_types].xml":
			hasTypes = true
			if f.UncompressedSize64 > 1<<20 {
				return "", errFileInvalid
			}
			rc, err := f.Open()
			if err != nil {
				return "", errFileInvalid
			}
			body, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
			_ = rc.Close()
			if bytes.Contains(bytes.ToLower(body), []byte("macroenabled")) {
				return "", errFileType
			}
		case strings.HasSuffix(name, "vbaproject.bin"):
			return "", errFileType
		case strings.HasPrefix(name, "word/"):
			family = docxMime
		case strings.HasPrefix(name, "xl/"):
			family = xlsxMime
		case strings.HasPrefix(name, "ppt/"):
			family = pptxMime
		}
	}
	if !hasTypes || family == "" {
		return "", errFileType // a plain zip (or anything else) is not on the list
	}
	return family, nil
}

// webpSize reads the canvas size from a WebP header (VP8, VP8L or VP8X).
func webpSize(d []byte) (w, h int, ok bool) {
	if len(d) < 30 {
		return 0, 0, false
	}
	switch string(d[12:16]) {
	case "VP8 ":
		return int(d[26]) | int(d[27]&0x3f)<<8, int(d[28]) | int(d[29]&0x3f)<<8, true
	case "VP8L":
		if d[20] != 0x2f {
			return 0, 0, false
		}
		b := uint32(d[21]) | uint32(d[22])<<8 | uint32(d[23])<<16 | uint32(d[24])<<24
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, true
	case "VP8X":
		return (int(d[24]) | int(d[25])<<8 | int(d[26])<<16) + 1, (int(d[27]) | int(d[28])<<8 | int(d[29])<<16) + 1, true
	}
	return 0, 0, false
}
