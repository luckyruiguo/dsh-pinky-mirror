package secret

import (
	"context"
	"errors"
	"testing"
)

func TestGetReadsOnlyModelKeys(t *testing.T) {
	m := &Memory{}
	if err := m.Save("pin:phone", "1234"); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(ModelKeyAccount("anthropic"), "sk-test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := Get(ctx, m, "pin:phone"); !errors.Is(err, ErrNotModelKey) || got != "" {
		t.Fatalf("Get(pin) = %q, %v; want ErrNotModelKey", got, err)
	}
	if got, err := Get(ctx, m, ModelKeyAccount("anthropic")); err != nil || got != "sk-test" {
		t.Fatalf("Get(model key) = %q, %v", got, err)
	}
	if _, err := Get(ctx, m, ModelKeyAccount("openai")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing model key) err = %v; want ErrNotFound", err)
	}
}
