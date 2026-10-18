package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The browser pre-checks a file before uploading (frontend/src/lib/team-chat.ts). The server decides, but
// the two lists must not drift: a limit raised on one side only gives the user a late or a wrong refusal.
// These tests read the TypeScript definitions themselves, so there is no second copy to keep in sync.
//
// Intentional differences, each asserted below rather than assumed:
//   - the paperclip does not offer the recorder-only containers (webm, mp4/m4a/aac);
//   - TEAM_CHAT_MIN_AUDIO_MS is UX only: the server has no minimum duration;
//   - the server has error codes the upload UI never shows (reactions, membership...).

func chatTS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "lib", "team-chat.ts"))
	require.NoError(t, err, "the TypeScript source of the chat rules must be readable from the Go tests")
	return string(b)
}

// tsNumber evaluates `export const NAME = 10 * 1024 * 1024` (integers joined by *), nothing fancier.
func tsNumber(t *testing.T, src, name string) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^export const ` + name + ` = ([^\n]+)$`).FindStringSubmatch(src)
	require.NotNil(t, m, "%s not found in team-chat.ts", name)
	expr := strings.TrimSpace(strings.SplitN(m[1], "//", 2)[0])
	n := 1
	for _, f := range strings.Split(expr, "*") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		require.NoError(t, err, "%s: %q is not a plain product of integers; extend tsNumber or use a shared fixture", name, expr)
		n *= v
	}
	return n
}

func tsStrings(t *testing.T, src, startPattern string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)` + startPattern + `(.*?)\]`).FindStringSubmatch(src)
	require.NotNil(t, m, "%q not found in team-chat.ts", startPattern)
	var out []string
	for _, s := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, s[1])
	}
	require.NotEmpty(t, out, "%q parsed to an empty list", startPattern)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestChatRulesParity_Limits(t *testing.T) {
	src := chatTS(t)
	assert.Equal(t, chatMaxFileBytes, tsNumber(t, src, "TEAM_CHAT_MAX_FILE_BYTES"), "bytes per file")
	assert.Equal(t, chatMaxAttachments, tsNumber(t, src, "TEAM_CHAT_MAX_ATTACHMENTS"), "attachments per message")
	assert.Equal(t, chatMaxAudioMillis, tsNumber(t, src, "TEAM_CHAT_MAX_AUDIO_MS"), "longest voice note")
}

func TestChatRulesParity_DangerousExtensions(t *testing.T) {
	src := chatTS(t)
	client := sortedCopy(tsStrings(t, src, `const DANGEROUS_EXT = new Set\(\[`))
	assert.Equal(t, sortedKeys(dangerousExt), client, "the refused extensions must be the same list on both sides")
}

// plainExts is every extension the server accepts from the paperclip: all known types except the two
// containers that may hold video, which it takes only when the sender claims a recording.
func plainExts() []string {
	recorderOnly := map[string]bool{"audio/webm": true, "audio/mp4": true}
	set := map[string]bool{}
	for mime, ty := range chatTypes {
		if recorderOnly[mime] {
			continue
		}
		for _, a := range ty.alts {
			set[a] = true
		}
	}
	return sortedKeys(set)
}

func TestChatRulesParity_AcceptedExtensions(t *testing.T) {
	src := chatTS(t)
	m := regexp.MustCompile(`(?m)^export const TEAM_CHAT_ACCEPT = '([^']*)'`).FindStringSubmatch(src)
	require.NotNil(t, m, "TEAM_CHAT_ACCEPT not found")
	var client []string
	for _, e := range strings.Split(m[1], ",") {
		require.True(t, strings.HasPrefix(e, "."), "%q must start with a dot", e)
		client = append(client, e[1:])
	}
	sort.Strings(client)
	assert.Equal(t, plainExts(), client, "the paperclip offers exactly what the server takes as a plain file")

	// Nothing the client offers may be a refused extension.
	for _, e := range client {
		assert.False(t, dangerousExt[e], "%s is offered but refused", e)
	}
}

// The exception above is real, not assumed: the same bytes are refused as a plain file and taken as a recording.
func TestChatRulesParity_RecorderOnlyContainers(t *testing.T) {
	webm := make([]byte, 64)
	copy(webm, []byte{0x1a, 0x45, 0xdf, 0xa3})
	copy(webm[20:], "webm")
	m4a := make([]byte, 32)
	copy(m4a[4:], "ftypM4A ")
	for name, data := range map[string][]byte{"a.webm": webm, "a.m4a": m4a} {
		_, err := inspectChatFile(data, name, false)
		assert.ErrorIs(t, err, errFileType, "%s as a plain file", name)
		info, err := inspectChatFile(data, name, true)
		require.NoError(t, err, "%s as a recording", name)
		assert.Equal(t, "audio", info.Kind)
	}
}

// TEAM_CHAT_MIN_AUDIO_MS is a UX rule of the recorder (a tap that records nothing is discarded). The server
// never receives a duration to judge: inspectChatFile has no such input, and the upload handler only clamps
// the declared duration to the maximum (see the 1 ms case in TestAttachments_AcceptedTypes).
func TestChatRulesParity_MinimumAudioIsUXOnly(t *testing.T) {
	src := chatTS(t)
	require.Greater(t, tsNumber(t, src, "TEAM_CHAT_MIN_AUDIO_MS"), 0)
	for _, f := range []string{"team_chat_attachments.go", "team_chat_attachments_validate.go"} {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotRegexp(t, `(?i)chatMinAudio|minAudio`, string(b), "%s: a server minimum would need a client counterpart", f)
	}
}

func TestChatRulesParity_ErrorCodes(t *testing.T) {
	src := chatTS(t)
	known := map[string]bool{}
	for _, c := range tsStrings(t, src, `export const TEAM_CHAT_UPLOAD_ERRORS = \[`) {
		known[c] = true
	}

	// Every code the upload endpoint can answer with is explained by the UI.
	for _, e := range []*chatHTTPError{
		errChatFileTooLarge, errChatFileType, errChatFileInvalid, errChatTooManyAtt,
		errChatAttGone, errChatRateLimited, errChatStorage,
	} {
		assert.True(t, known[e.code], "upload error %q is missing from TEAM_CHAT_UPLOAD_ERRORS", e.code)
	}

	// And every code the UI claims to explain is one the server really sends.
	var goSrc strings.Builder
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		goSrc.Write(b)
	}
	for code := range known {
		assert.Contains(t, goSrc.String(), `"`+code+`"`, "TEAM_CHAT_UPLOAD_ERRORS lists %q but no handler sends it", code)
	}
}
