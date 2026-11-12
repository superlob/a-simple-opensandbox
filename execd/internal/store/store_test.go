package store

import (
	"errors"
	"testing"
	"time"
)

const validID = "11111111-2222-4333-8444-555555555555"

func TestRecordRoundTripAndList(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.SaveRecord(Record{ID: validID, Status: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	r, err := st.LoadRecord(validID)
	if err != nil || r.Status != "running" {
		t.Fatalf("load: %+v %v", r, err)
	}
	recs, skipped, err := st.ListRecords()
	if err != nil || len(recs) != 1 || skipped != 0 {
		t.Errorf("list: %d %d %v", len(recs), skipped, err)
	}
	if _, err := st.LoadRecord("../../etc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("path-like id: %v", err)
	}
}

func TestReadLogPaging(t *testing.T) {
	st, _ := New(t.TempDir())
	f, err := st.OpenLog(validID, StreamStdout)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("0123456789")
	f.Close()
	data, next, eof, err := st.ReadLog(validID, StreamStdout, 0, 4)
	if err != nil || string(data) != "0123" || next != 4 || eof {
		t.Errorf("page1 %q %d %v %v", data, next, eof, err)
	}
	data, next, eof, _ = st.ReadLog(validID, StreamStdout, 4, 100)
	if string(data) != "456789" || next != 10 || !eof {
		t.Errorf("page2 %q %d %v", data, next, eof)
	}
	data, _, eof, err = st.ReadLog(validID, StreamStderr, 0, 10)
	if err != nil || len(data) != 0 || !eof {
		t.Errorf("missing log: %q %v %v", data, eof, err)
	}
}
