package web

import (
	"errors"
	"testing"
)

func TestRemoveCaptureIfNotPublishingCoordinatesOnly(t *testing.T) {
	dir := t.TempDir()

	finish := BeginCapturePublish(dir, "x.wav")
	ran := false
	err := RemoveCaptureIfNotPublishing(dir, "x.wav", func() error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrCaptureBusy) {
		t.Fatalf("remove during publish = %v, want ErrCaptureBusy", err)
	}
	if ran {
		t.Fatal("remove closure ran while publish held")
	}
	finish()

	if err := RemoveCaptureIfNotPublishing(dir, "x.wav", func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("remove after publish finished: %v", err)
	}
	if !ran {
		t.Fatal("remove closure did not run after publish finished")
	}

	boom := errors.New("boom")
	if err := RemoveCaptureIfNotPublishing(dir, "x.wav", func() error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("closure error = %v, want boom", err)
	}
}
