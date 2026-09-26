package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPSProbeRecorderRejectsUnboundedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &bpsProbeRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	n, err := recorder.WriteString(strings.Repeat("x", bpsAccountProbeMaxResponseBytes))
	require.NoError(t, err)
	require.Equal(t, bpsAccountProbeMaxResponseBytes, n)
	n, err = recorder.Write([]byte("overflow"))
	require.ErrorIs(t, err, errBPSProbeResponseTooLarge)
	require.Zero(t, n)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, bpsAccountProbeMaxResponseBytes, recorder.Body.Len())
	_, err = recorder.WriteString("ignored after cancellation")
	require.ErrorIs(t, err, errBPSProbeResponseTooLarge)
}
