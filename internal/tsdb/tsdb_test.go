package tsdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestWriteAndQuery(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now.Add(-2 * time.Minute), Metric: "latency", ModelID: "m1", Value: 100})
	s.Write(Point{Timestamp: now.Add(-1 * time.Minute), Metric: "latency", ModelID: "m1", Value: 150})
	s.Write(Point{Timestamp: now, Metric: "latency", ModelID: "m1", Value: 200})

	series, err := s.Query(context.Background(), QueryParams{Metric: "latency"})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}
	if len(series[0].Points) != 3 {
		t.Errorf("expected 3 points, got %d", len(series[0].Points))
	}
	if series[0].ModelID != "m1" {
		t.Errorf("expected model m1, got %s", series[0].ModelID)
	}
}

func TestQueryWithTimeRange(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now.Add(-10 * time.Minute), Metric: "cost", Value: 0.01})
	s.Write(Point{Timestamp: now.Add(-5 * time.Minute), Metric: "cost", Value: 0.02})
	s.Write(Point{Timestamp: now, Metric: "cost", Value: 0.03})

	series, err := s.Query(context.Background(), QueryParams{
		Metric: "cost",
		Start:  now.Add(-6 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}
	if len(series[0].Points) != 2 {
		t.Errorf("expected 2 points after time filter, got %d", len(series[0].Points))
	}
}

func TestQueryGroupsByModelAndProvider(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now, Metric: "latency", ModelID: "m1", ProviderID: "p1", Value: 100})
	s.Write(Point{Timestamp: now, Metric: "latency", ModelID: "m2", ProviderID: "p2", Value: 200})

	series, err := s.Query(context.Background(), QueryParams{Metric: "latency"})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 series (different models), got %d", len(series))
	}
}

func TestQueryFilterByModel(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now, Metric: "latency", ModelID: "m1", Value: 100})
	s.Write(Point{Timestamp: now, Metric: "latency", ModelID: "m2", Value: 200})

	series, err := s.Query(context.Background(), QueryParams{Metric: "latency", ModelID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 series for m1, got %d", len(series))
	}
	if series[0].Points[0].Value != 100 {
		t.Errorf("expected value 100, got %f", series[0].Points[0].Value)
	}
}

func TestDownsample(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Minute)
	// Write 6 points in the same minute bucket.
	for i := range 6 {
		s.Write(Point{
			Timestamp: now.Add(time.Duration(i) * 10 * time.Second),
			Metric:    "latency",
			ModelID:   "m1",
			Value:     float64(100 + i*10),
		})
	}

	series, err := s.Query(context.Background(), QueryParams{
		Metric: "latency",
		StepMs: 60000, // 1 minute buckets
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}
	// All 6 points should be averaged into 1 bucket.
	if len(series[0].Points) != 1 {
		t.Errorf("expected 1 downsampled point, got %d", len(series[0].Points))
	}
	// Average of 100,110,120,130,140,150 = 125
	if series[0].Points[0].Value != 125 {
		t.Errorf("expected avg 125, got %f", series[0].Points[0].Value)
	}
}

func TestPrune(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRetention(time.Hour)

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now.Add(-2 * time.Hour), Metric: "old", Value: 1})
	s.Write(Point{Timestamp: now, Metric: "new", Value: 2})

	deleted, err := s.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	series, err := s.Query(context.Background(), QueryParams{Metric: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Error("expected new point to survive pruning")
	}
}

func TestMetrics(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now, Metric: "latency", Value: 100})
	s.Write(Point{Timestamp: now, Metric: "cost", Value: 0.01})
	s.Write(Point{Timestamp: now, Metric: "latency", Value: 200})

	metrics, err := s.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 distinct metrics, got %d", len(metrics))
	}
}

func TestBufferFlush(t *testing.T) {
	db := testDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.bufMax = 3 // small buffer for testing

	now := time.Now().UTC()
	s.Write(Point{Timestamp: now, Metric: "test", Value: 1})
	s.Write(Point{Timestamp: now, Metric: "test", Value: 2})
	// Buffer not yet flushed - query forces flush.
	series, err := s.Query(context.Background(), QueryParams{Metric: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 || len(series[0].Points) != 2 {
		t.Error("expected 2 points after query-triggered flush")
	}
}
