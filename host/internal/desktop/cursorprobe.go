//go:build cursorprobe

package desktop

import "context"

// MovePointer moves the pointer by dx, dy through the remote desktop session.
func (c *Capture) MovePointer(ctx context.Context, dx, dy float64) error {
	return c.conn.Object(mutterRemoteBus, c.remote).CallWithContext(ctx, ifaceRemoteSession+".NotifyPointerMotionRelative", 0, dx, dy).Err
}
