package voice

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/chaim12345/sdrstream/go/internal/rivaspeech"
)

// slowRiva is a fake NIM endpoint whose Recognize blocks briefly and
// records the peak number of simultaneous calls it served.
type slowRiva struct {
	rivaspeech.UnimplementedRivaSpeechRecognitionServer
	delay time.Duration
	cur   atomic.Int32
	max   atomic.Int32
	fail  atomic.Bool // when set, Recognize returns codes.Internal
}

func (s *slowRiva) Recognize(ctx context.Context, request *rivaspeech.RecognizeRequest) (*rivaspeech.RecognizeResponse, error) {
	cur := s.cur.Add(1)
	for {
		if m := s.max.Load(); cur <= m || s.max.CompareAndSwap(m, cur) {
			break
		}
	}
	defer s.cur.Add(-1)
	time.Sleep(s.delay)
	if s.fail.Load() {
		return nil, status.Error(codes.Internal, "boom")
	}
	return &rivaspeech.RecognizeResponse{
		Results: []*rivaspeech.SpeechRecognitionResult{{
			Alternatives: []*rivaspeech.SpeechRecognitionAlternative{{Transcript: "hola", Confidence: 0.9}},
		}},
	}, nil
}

func dialSlow(t *testing.T, server rivaspeech.RivaSpeechRecognitionServer) {
	t.Helper()
	nimResetForTests()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	rivaspeech.RegisterRivaSpeechRecognitionServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	previous := nimDial
	nimDial = func(ctx context.Context, options NIMOptions) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, "bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	t.Cleanup(func() {
		nimDial = previous
		grpcServer.Stop()
		listener.Close()
		nimResetForTests()
	})
}

// The NIM semaphore caps concurrent recognize calls against the catalog
// tier: more callers than slots must queue, never burst past the cap.
func TestNIMSemaphoreBoundsConcurrency(t *testing.T) {
	server := &slowRiva{delay: 120 * time.Millisecond}
	dialSlow(t, server)
	callers := nimMaxConcurrent + 3
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = TranscribeNIM(context.Background(), toneSamples(8000), 8000, testNIMOptions())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if max := server.max.Load(); max > int32(nimMaxConcurrent) {
		t.Errorf("server saw %d concurrent recognizes, cap is %d", max, nimMaxConcurrent)
	}
	if max := server.max.Load(); max < int32(nimMaxConcurrent) {
		t.Errorf("server peak was %d, want the %d slots actually used", max, nimMaxConcurrent)
	}
}

// A caller cancelled while waiting for a free NIM slot must fail fast
// with the ctx error and must not consume a slot.
func TestNIMSemaphoreCancelWhileWaiting(t *testing.T) {
	server := &slowRiva{delay: time.Millisecond}
	dialSlow(t, server)
	for i := 0; i < nimMaxConcurrent; i++ {
		nimSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := TranscribeNIM(ctx, toneSamples(8000), 8000, testNIMOptions())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("queued cancel err = %v, want context.Canceled", err)
	}
	if err != nil && !strings.Contains(err.Error(), "waiting for a free slot") {
		t.Errorf("error %v does not name the slot wait", err)
	}
	if len(nimSlots) != nimMaxConcurrent {
		t.Errorf("queued caller disturbed the slots: len = %d, want %d", len(nimSlots), nimMaxConcurrent)
	}
	for i := 0; i < nimMaxConcurrent; i++ {
		<-nimSlots
	}
	// Slots healthy afterwards: a normal call completes.
	if _, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, testNIMOptions()); err != nil {
		t.Errorf("post-cancel call: %v", err)
	}
}

// A failed recognize must release its slot (and a non-quota failure must
// not arm the cooldown), so the next caller proceeds.
func TestNIMSemaphoreErrorRelease(t *testing.T) {
	server := &slowRiva{delay: time.Millisecond}
	server.fail.Store(true)
	dialSlow(t, server)
	if _, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, testNIMOptions()); err == nil {
		t.Fatal("failing server reported success")
	}
	if remaining := nimCooldownRemaining(); remaining > 0 {
		t.Fatalf("codes.Internal armed a %s cooldown; only quota/auth/outage failures may", remaining)
	}
	server.fail.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transcript, err := TranscribeNIM(ctx, toneSamples(8000), 8000, testNIMOptions())
	if err != nil || transcript.Text != "hola" {
		t.Errorf("call after error: text %q err %v (slot leaked?)", transcript.Text, err)
	}
}
