package calling

import (
	"errors"
	"sync/atomic"
)

// ErrAudioNotApproved: the file has no clean antimalware verdict (or is outside the audio directory), so it
// is not played. The call carries on in silence at that point of the flow: nothing is scanned during a call.
var ErrAudioNotApproved = errors.New("audio file is not approved for playback")

// AudioGate decides, from memory and without any I/O, whether a file may be played into a call. The
// approval is prepared beforehand (scan on upload, background scan of older files): a call never waits
// for a scan.
type AudioGate interface {
	Approved(path string) bool
}

type gateHolder struct{ g AudioGate }

var audioGate atomic.Pointer[gateHolder]

// SetAudioGate installs the gate every playback goes through (PlayFile and PlayFileLoop). The server
// installs it at startup; with none installed (tests of the media path alone) playback is unrestricted.
func SetAudioGate(g AudioGate) {
	if g == nil {
		audioGate.Store(nil)
		return
	}
	audioGate.Store(&gateHolder{g})
}

func audioApproved(path string) bool {
	h := audioGate.Load()
	return h == nil || h.g.Approved(path)
}
