/* Resolve real GTK/WebKit symbols so dependency tests use the real distro ABI.
 * The test compiles and inspects this fixture; it never executes it. */
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
int main(void) {
    return (int)(gtk_get_major_version() + webkit_get_major_version());
}
