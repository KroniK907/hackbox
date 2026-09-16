package store

import (
	"context"
	"testing"
)

func TestGameKVIsNamespacedAndClearDeletesOneGame(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	ctx := context.Background()

	if err := db.KVSet(ctx, "fake", "score", []byte("12")); err != nil {
		t.Fatal(err)
	}
	if err := db.KVSet(ctx, "other", "score", []byte("99")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.KVGet(ctx, "fake", "score")
	if err != nil || !ok || string(got) != "12" {
		t.Fatalf("fake score = %q ok=%v err=%v", got, ok, err)
	}

	if err := db.KVDeleteGame(ctx, "fake"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = db.KVGet(ctx, "fake", "score")
	if err != nil || ok {
		t.Fatalf("cleared fake still present ok=%v err=%v", ok, err)
	}
	got, ok, err = db.KVGet(ctx, "other", "score")
	if err != nil || !ok || string(got) != "99" {
		t.Fatalf("other score = %q ok=%v err=%v", got, ok, err)
	}
}
