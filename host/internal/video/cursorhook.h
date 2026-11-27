// Declarations of cursorhook.c for cursor.go.
#include <stdint.h>

// desklink_cursor_inspect reports the pointer metadata of one spa_buffer.
void desklink_cursor_inspect(void *spa_buffer);
// desklink_cursor_hook_exported is 1 when the executable exports the hook,
// so that the pipewire plugin calls it.
int desklink_cursor_hook_exported(void);
// desklink_cursor_hook_buffers counts the buffers the hook has seen.
long desklink_cursor_hook_buffers(void);
// desklink_cursor_forget makes the next position be reported even if it is
// the last one reported, for a new cast.
void desklink_cursor_forget(void);
// desklink_cursor_test_buffer builds a spa_buffer with pointer metadata for
// tests; free it with free().
void *desklink_cursor_test_buffer(int x, int y, int w, int h, int hx, int hy, uint32_t format, int empty);
