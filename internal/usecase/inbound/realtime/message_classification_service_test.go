package realtime

import (
	"context"
	"testing"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"

	"github.com/google/uuid"
)

type stubTextScanGate struct {
	enabled bool
	calls   int
}

func (g *stubTextScanGate) HasChannelRealtimeTextScanService(ctx context.Context, channelID uuid.UUID) (bool, error) {
	g.calls++
	return g.enabled, nil
}

type stubClassifier struct {
	calls  int
	result *ClassificationResult
}

func (c *stubClassifier) Classify(ctx context.Context, text string) (*ClassificationResult, error) {
	c.calls++
	if c.result != nil {
		return c.result, nil
	}
	return &ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate}, nil
}

type stubClassificationHandler struct {
	calls int
}

func (h *stubClassificationHandler) HandleClassification(ctx context.Context, messageCtx MessageContext, result ClassificationResult) {
	h.calls++
}

func TestMessageClassificationServiceSkipsClassifierWithoutHandlers(t *testing.T) {
	gate := &stubTextScanGate{enabled: true}
	classifier := &stubClassifier{}
	service := NewMessageClassificationService(MessageClassificationServiceOptions{
		TextScanGate: gate,
		Classifier:   classifier,
	})

	service.Handle(context.Background(), textMessageContext())

	if gate.calls != 0 {
		t.Fatalf("expected gate not to be called without handlers, got %d", gate.calls)
	}
	if classifier.calls != 0 {
		t.Fatalf("expected classifier not to be called without handlers, got %d", classifier.calls)
	}
}

func TestMessageClassificationServiceSkipsClassifierWhenChannelHasNoTextScanService(t *testing.T) {
	gate := &stubTextScanGate{enabled: false}
	classifier := &stubClassifier{}
	handler := &stubClassificationHandler{}
	service := NewMessageClassificationService(MessageClassificationServiceOptions{
		TextScanGate: gate,
		Classifier:   classifier,
		Handlers:     []ClassificationHandler{handler},
	})

	service.Handle(context.Background(), textMessageContext())

	if gate.calls != 1 {
		t.Fatalf("expected gate to be called once, got %d", gate.calls)
	}
	if classifier.calls != 0 {
		t.Fatalf("expected classifier not to be called, got %d", classifier.calls)
	}
	if handler.calls != 0 {
		t.Fatalf("expected handler not to be called, got %d", handler.calls)
	}
}

func TestMessageClassificationServiceClassifiesWhenChannelHasTextScanService(t *testing.T) {
	gate := &stubTextScanGate{enabled: true}
	classifier := &stubClassifier{}
	handler := &stubClassificationHandler{}
	service := NewMessageClassificationService(MessageClassificationServiceOptions{
		TextScanGate: gate,
		Classifier:   classifier,
		Handlers:     []ClassificationHandler{handler},
	})

	service.Handle(context.Background(), textMessageContext())

	if gate.calls != 1 {
		t.Fatalf("expected gate to be called once, got %d", gate.calls)
	}
	if classifier.calls != 1 {
		t.Fatalf("expected classifier to be called once, got %d", classifier.calls)
	}
	if handler.calls != 1 {
		t.Fatalf("expected handler to be called once, got %d", handler.calls)
	}
}

func TestMessageClassificationServiceDispatchesUnclearSignal(t *testing.T) {
	gate := &stubTextScanGate{enabled: true}
	classifier := &stubClassifier{result: &ClassificationResult{Tag: "none", Signal: ClassificationSignalUnclear}}
	handler := &stubClassificationHandler{}
	service := NewMessageClassificationService(MessageClassificationServiceOptions{
		TextScanGate: gate,
		Classifier:   classifier,
		Handlers:     []ClassificationHandler{handler},
	})

	service.Handle(context.Background(), textMessageContext())

	if classifier.calls != 1 {
		t.Fatalf("expected classifier to be called once, got %d", classifier.calls)
	}
	if handler.calls != 1 {
		t.Fatalf("expected handler to be called for unclear signal, got %d", handler.calls)
	}
}

func TestMessageClassificationServiceSkipsHandlerOnRejectSignal(t *testing.T) {
	gate := &stubTextScanGate{enabled: true}
	classifier := &stubClassifier{result: &ClassificationResult{Tag: "none", Signal: ClassificationSignalReject}}
	handler := &stubClassificationHandler{}
	service := NewMessageClassificationService(MessageClassificationServiceOptions{
		TextScanGate: gate,
		Classifier:   classifier,
		Handlers:     []ClassificationHandler{handler},
	})

	service.Handle(context.Background(), textMessageContext())

	if classifier.calls != 1 {
		t.Fatalf("expected classifier to be called once, got %d", classifier.calls)
	}
	if handler.calls != 0 {
		t.Fatalf("expected handler not to be called for reject signal, got %d", handler.calls)
	}
}

func textMessageContext() MessageContext {
	return MessageContext{
		Message: &unifiedmessage.Message{
			ChannelID:         "channel-1",
			PlatformMessageID: "message-1",
			MessageType:       "text",
			Text:              "remind me tomorrow",
		},
		SavedMessage: &ent.ChannelMessage{ChannelID: uuid.New()},
	}
}
