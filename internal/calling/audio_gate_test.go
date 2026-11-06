package calling

import (
	"errors"
	"testing"
)

type denyAll struct{ asked []string }

func (d *denyAll) Approved(p string) bool { d.asked = append(d.asked, p); return false }

type allowAll struct{}

func (allowAll) Approved(string) bool { return true }

func TestPlayFile_IsGated(t *testing.T) {
	t.Cleanup(func() { SetAudioGate(nil) })
	p := NewAudioPlayer(nil)

	d := &denyAll{}
	SetAudioGate(d)
	n, err := p.PlayFile("/no/such/greeting.ogg")
	if !errors.Is(err, ErrAudioNotApproved) || n != 0 {
		t.Fatalf("an unapproved file must not even be opened: %d %v", n, err)
	}
	// the loop variant goes through PlayFile on every iteration
	if err := p.PlayFileLoop("/no/such/hold.ogg"); !errors.Is(err, ErrAudioNotApproved) {
		t.Fatalf("loop: %v", err)
	}
	if len(d.asked) != 2 {
		t.Fatalf("the gate was asked %d times", len(d.asked))
	}

	SetAudioGate(allowAll{}) // approved: the playback proceeds to open the file (which does not exist here)
	if _, err := p.PlayFile("/no/such/greeting.ogg"); err == nil || errors.Is(err, ErrAudioNotApproved) {
		t.Fatalf("an approved file reaches the file layer: %v", err)
	}
}
