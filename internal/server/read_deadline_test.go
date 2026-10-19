package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProgressDeadlinePreservesTighterCompleteResponseBound(t *testing.T) {
	underlying := &recordingDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := newProgressDeadlineResponseWriter(underlying)
	maximum := time.Now().Add(time.Second)
	if err := http.NewResponseController(writer).SetWriteDeadline(maximum); err != nil {
		t.Fatal(err)
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("first progress"))
	_, _ = writer.Write([]byte("later progress"))
	if len(underlying.deadlines) != 4 {
		t.Fatalf("deadline was not applied to each write: %v", underlying.deadlines)
	}
	for _, deadline := range underlying.deadlines {
		if !deadline.Equal(maximum) {
			t.Fatal("response progress extended the complete-request deadline")
		}
	}
	writer.clear()
	if !underlying.deadlines[len(underlying.deadlines)-1].IsZero() {
		t.Fatal("request deadline was not cleared")
	}
	if err := http.NewResponseController(writer).SetReadDeadline(maximum); err != nil {
		t.Fatal(err)
	}
	writer.resetRead()
	writer.resetRead()
	if len(underlying.readDeadlines) != 3 {
		t.Fatal("read deadline was not applied to progress")
	}
	for _, deadline := range underlying.readDeadlines {
		if !deadline.Equal(maximum) {
			t.Fatal("request progress extended the complete-request deadline")
		}
	}
	if err := http.NewResponseController(writer).SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !underlying.readDeadlines[len(underlying.readDeadlines)-1].IsZero() {
		t.Fatal("explicit read deadline was not cleared")
	}
}

type recordingDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines     []time.Time
	readDeadlines []time.Time
}

func (writer *recordingDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	writer.readDeadlines = append(writer.readDeadlines, deadline)
	return nil
}

func (writer *recordingDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	writer.deadlines = append(writer.deadlines, deadline)
	return nil
}
