package skills

import (
	"context"
	"strings"
	"testing"
)

func TestRuntimeConsumerSmokeRequiresManagedSessionOpener(t *testing.T) {
	if _, err := RunRuntimeConsumerSmoke(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "managed session opener") {
		t.Fatalf("nil opener error = %v", err)
	}
}
