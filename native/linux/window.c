#define _POSIX_C_SOURCE 200809L
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include <json-glib/json-glib.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#define MAX_LINE (16 * 1024)
#define DRAG_WORLD "YuDeskNativeWindow"

typedef struct {
    GtkWidget *window;
    WebKitWebView *web;
    WebKitWebContext *context;
    gint port;
    gchar *initial_url;
    GByteArray *pending;
    gboolean closing, loaded, failed;
    guint reloads, reload_source, timeout_source;
    gdouble press_x, press_y;
    guint32 press_time;
    gint64 press_monotonic;
    gboolean pressed, fullscreen;
    gint64 action_time;
    GtkNativeDialog *file_dialog;
#ifdef YUDESK_WINDOW_TEST
    gchar *test_download_destination;
#endif
} Shell;

static void emit(const char *event, const char *code) {
    // Only fixed internal strings reach this function, never a URI/error/page value.
    if (code) printf("{\"event\":\"%s\",\"code\":\"%s\"}\n", event, code);
    else printf("{\"event\":\"%s\"}\n", event);
    fflush(stdout);
}

static gboolean token_valid(const char *query) {
    if (!query) return FALSE;
    gchar **pairs = g_strsplit(query, "&", -1);
    guint count = 0;
    gboolean valid = TRUE;
    for (guint i = 0; pairs[i]; ++i) {
        gchar **pair = g_strsplit(pairs[i], "=", 2);
        gchar *name = g_uri_unescape_string(pair[0], NULL);
        if (name && !strcmp(name, "access_token")) {
            ++count;
            gchar *value = pair[1] ? g_uri_unescape_string(pair[1], NULL) : NULL;
            if (!value || !value[0] || !g_utf8_validate(value, -1, NULL)) valid = FALSE;
            for (const guchar *p = (const guchar *)value; p && *p; ++p)
                if (*p < 33 || *p == 127) valid = FALSE;
            g_free(value);
        }
        g_free(name); g_strfreev(pair);
    }
    g_strfreev(pairs);
    return valid && count == 1;
}

static gint local_port(const char *raw, gboolean require_token, gboolean allow_fragment) {
    if (!raw || strlen(raw) > MAX_LINE || !g_str_has_prefix(raw, "http://127.0.0.1:")) return -1;
    for (const guchar *p = (const guchar *)raw; *p; ++p) if (*p <= 32 || *p == 127) return -1;
    // Preserve encoded query for single decoding and reject embedded NUL tokens.
    GUri *uri = g_uri_parse(raw, G_URI_FLAGS_ENCODED_QUERY, NULL);
    if (!uri) return -1;
    gint port = g_uri_get_port(uri);
    gboolean valid = !g_strcmp0(g_uri_get_scheme(uri), "http") &&
        !g_strcmp0(g_uri_get_host(uri), "127.0.0.1") && !g_uri_get_userinfo(uri) &&
        (allow_fragment || !g_uri_get_fragment(uri)) && port >= 1 && port <= 65535 &&
        (!require_token || token_valid(g_uri_get_query(uri)));
    g_uri_unref(uri);
    return valid ? port : -1;
}

static gboolean permitted(Shell *s, const char *uri) {
    return s->port > 0 && local_port(uri, FALSE, TRUE) == s->port;
}
static void stop_shell(Shell *s) {
    s->closing = TRUE;
    if (s->file_dialog) gtk_native_dialog_destroy(s->file_dialog);
    gtk_main_quit();
}
static void failure(Shell *s, const char *code) {
    if (!s->failed) { s->failed = TRUE; emit("error", code); }
}
static void show_window(Shell *s) {
    gtk_widget_show_all(s->window);
    gtk_window_set_skip_taskbar_hint(GTK_WINDOW(s->window), FALSE);
    gtk_window_deiconify(GTK_WINDOW(s->window)); gtk_window_present(GTK_WINDOW(s->window));
}
static gboolean deleted(GtkWidget *widget, GdkEvent *event, gpointer data) {
    (void)widget; (void)event;
    Shell *s = data;
    if (!s->closing) emit("closed", NULL);
    stop_shell(s); return TRUE;
}
static gboolean press(GtkWidget *widget, GdkEventButton *event, gpointer data) {
    (void)widget;
    Shell *s = data;
    if (event->button == 1 && event->type == GDK_BUTTON_PRESS) {
        gint width = gtk_widget_get_allocated_width(widget), height = gtk_widget_get_allocated_height(widget);
        gboolean left = event->x < 5, right = event->x >= width - 5;
        gboolean top = event->y < 5, bottom = event->y >= height - 5;
        if (left || right || top || bottom) {
            GdkWindowEdge edge = top ? (left ? GDK_WINDOW_EDGE_NORTH_WEST : right ? GDK_WINDOW_EDGE_NORTH_EAST : GDK_WINDOW_EDGE_NORTH)
                : bottom ? (left ? GDK_WINDOW_EDGE_SOUTH_WEST : right ? GDK_WINDOW_EDGE_SOUTH_EAST : GDK_WINDOW_EDGE_SOUTH)
                : left ? GDK_WINDOW_EDGE_WEST : GDK_WINDOW_EDGE_EAST;
            gtk_window_begin_resize_drag(GTK_WINDOW(s->window), edge, 1, (gint)event->x_root, (gint)event->y_root, event->time);
            return TRUE;
        }
        s->pressed = TRUE; s->press_x = event->x_root; s->press_y = event->y_root;
        s->press_time = event->time; s->press_monotonic = g_get_monotonic_time();
        s->action_time = s->press_monotonic;
    }
    return FALSE;
}
static gboolean release(GtkWidget *widget, GdkEventButton *event, gpointer data) {
    (void)widget;
    Shell *s = data;
    if (event->button == 1) s->pressed = FALSE;
    return FALSE;
}
static void fullscreen_label(Shell *s) {
    if (!s->web || !permitted(s, webkit_web_view_get_uri(s->web))) return;
    const char *script = s->fullscreen
        ? "(()=>{const b=document.getElementById('fullscreen');if(b){b.textContent='退出全屏';b.setAttribute('aria-pressed','true');b.title='原生窗口全屏，Esc 退出';}})()"
        : "(()=>{const b=document.getElementById('fullscreen');if(b){b.textContent='整屏显示';b.setAttribute('aria-pressed','false');b.title='原生窗口全屏';}})()";
    webkit_web_view_evaluate_javascript(s->web, script, -1, NULL, NULL, NULL, NULL, NULL);
}
static gboolean window_state(GtkWidget *widget, GdkEventWindowState *event, gpointer data) {
    (void)widget;
    Shell *s = data;
    if (event->changed_mask & GDK_WINDOW_STATE_FULLSCREEN) {
        s->fullscreen = (event->new_window_state & GDK_WINDOW_STATE_FULLSCREEN) != 0;
        fullscreen_label(s);
    }
    return FALSE;
}
static gboolean key_press(GtkWidget *widget, GdkEventKey *event, gpointer data) {
    (void)widget;
    Shell *s = data;
    if (event->keyval == GDK_KEY_Return || event->keyval == GDK_KEY_KP_Enter ||
        event->keyval == GDK_KEY_space || event->keyval == GDK_KEY_Escape)
        s->action_time = g_get_monotonic_time();
    return FALSE;
}
static void drag_message(WebKitUserContentManager *manager, WebKitJavascriptResult *result, gpointer data) {
    (void)manager;
    Shell *s = data;
    JSCValue *value = webkit_javascript_result_get_js_value(result);
    if (!jsc_value_is_string(value) || !permitted(s, webkit_web_view_get_uri(s->web))) return;
    gchar *text = jsc_value_to_string(value);
    if (!g_strcmp0(text, "fullscreen") || !g_strcmp0(text, "unfullscreen")) {
        gboolean exit = !strcmp(text, "unfullscreen");
        g_free(text);
        if (!s->action_time || g_get_monotonic_time() - s->action_time > 1000000) return;
        s->action_time = 0; // A native input can authorize at most one action.
        if (exit && !s->fullscreen) return;
        s->fullscreen = !exit && !s->fullscreen;
        if (s->fullscreen) gtk_window_fullscreen(GTK_WINDOW(s->window));
        else gtk_window_unfullscreen(GTK_WINDOW(s->window));
        fullscreen_label(s);
        return;
    }
    gboolean valid = !g_strcmp0(text, "drag"); g_free(text);
    if (!valid || !s->pressed || g_get_monotonic_time() - s->press_monotonic > 500000) return;
    s->pressed = FALSE;
    gtk_window_begin_move_drag(GTK_WINDOW(s->window), 1, (gint)s->press_x, (gint)s->press_y, s->press_time);
}
static gboolean policy(WebKitWebView *web, WebKitPolicyDecision *decision,
                       WebKitPolicyDecisionType type, gpointer data) {
    (void)web;
    Shell *s = data;
    if (type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
        webkit_policy_decision_ignore(decision); return TRUE;
    }
    if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION) {
        WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(decision));
        const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(action));
        if (!permitted(s, uri)) { webkit_policy_decision_ignore(decision); return TRUE; }
        webkit_policy_decision_use(decision); return TRUE;
    }
    if (type == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
        WebKitResponsePolicyDecision *response = WEBKIT_RESPONSE_POLICY_DECISION(decision);
        const char *uri = webkit_uri_response_get_uri(webkit_response_policy_decision_get_response(response));
        if (!permitted(s, uri)) webkit_policy_decision_ignore(decision);
        else if (webkit_uri_response_get_status_code(webkit_response_policy_decision_get_response(response)) >= 400) {
            failure(s, "load_http_error"); webkit_policy_decision_ignore(decision);
        }
        else if (!webkit_response_policy_decision_is_mime_type_supported(response)) webkit_policy_decision_download(decision);
        else webkit_policy_decision_use(decision);
        return TRUE;
    }
    return FALSE;
}
static void features_checked(GObject *source, GAsyncResult *result, gpointer data) {
    Shell *s = data;
    JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(source), result, NULL);
    gboolean ok = value && jsc_value_is_boolean(value) && jsc_value_to_boolean(value);
    if (value) g_object_unref(value);
    if (s->closing || s->failed) return;
    if (!ok) { failure(s, "unsupported_webkit"); return; }
    s->loaded = TRUE; emit("ready", NULL);
    fullscreen_label(s);
}
static void loaded(WebKitWebView *web, WebKitLoadEvent event, gpointer data) {
    Shell *s = data;
    if (event == WEBKIT_LOAD_STARTED) s->failed = FALSE;
    if (event == WEBKIT_LOAD_FINISHED && !s->failed && permitted(s, webkit_web_view_get_uri(web))) {
        webkit_web_view_evaluate_javascript(web,
            "typeof fetch==='function' && typeof createImageBitmap==='function' && typeof AudioContext==='function' && typeof AbortSignal.timeout==='function' && typeof HTMLDialogElement==='function' && typeof HTMLDialogElement.prototype.showModal==='function' && !!document.createElement('canvas').getContext('2d')",
            -1, NULL, NULL, NULL, features_checked, s);
    }
}
static gboolean load_failed(WebKitWebView *web, WebKitLoadEvent event, gchar *uri, GError *error, gpointer data) {
    (void)web; (void)event; (void)uri;
    Shell *s = data;
    if (!g_error_matches(error, WEBKIT_NETWORK_ERROR, WEBKIT_NETWORK_ERROR_CANCELLED)) failure(s, "load_failed");
    return TRUE; // Do not display a WebKit error page containing the bearer URL.
}
static gboolean load_timeout(gpointer data) {
    Shell *s = data; s->timeout_source = 0;
    if (!s->loaded) failure(s, "load_timeout");
    return G_SOURCE_REMOVE;
}
static gboolean reload_page(gpointer data) {
    Shell *s = data; s->reload_source = 0;
    if (!s->closing) webkit_web_view_load_uri(s->web, s->initial_url);
    return G_SOURCE_REMOVE;
}
static void terminated(WebKitWebView *web, WebKitWebProcessTerminationReason reason, gpointer data) {
    (void)web; (void)reason;
    Shell *s = data; emit("error", "web_process_terminated");
    // A process-lifetime cap, not reset by successful loads, prevents crash loops.
    if (s->reloads < 2 && !s->reload_source && !s->closing)
        s->reload_source = g_timeout_add_seconds(++s->reloads, reload_page, s);
}
static gboolean script_dialog(WebKitWebView *web, WebKitScriptDialog *dialog, gpointer data) {
    (void)web; (void)data;
    emit("error", "unexpected_script_dialog");
    WebKitScriptDialogType type = webkit_script_dialog_get_dialog_type(dialog);
    if (type == WEBKIT_SCRIPT_DIALOG_CONFIRM || type == WEBKIT_SCRIPT_DIALOG_BEFORE_UNLOAD_CONFIRM)
        webkit_script_dialog_confirm_set_confirmed(dialog, FALSE);
    // Returning TRUE handles without a browser/native alert; app uses HTML dialogs.
    return TRUE;
}
static gboolean permission(WebKitWebView *web, WebKitPermissionRequest *request, gpointer data) {
    (void)web; (void)data;
    // Rendering remote media needs no camera/microphone, location or notifications.
    // Clipboard remains WebKit's user-gesture API; do not auto-grant new devices.
    webkit_permission_request_deny(request); return TRUE;
}
static void transfer_status(Shell *s, const char *state) {
    if (!s->web || !permitted(s, webkit_web_view_get_uri(s->web))) return;
    const char *script;
    if (!strcmp(state, "complete")) script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='文件已保存';e.hidden=false;}})()";
    else if (!strcmp(state, "cancelled")) script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='已取消保存文件';e.hidden=false;}})()";
    else if (!strcmp(state, "saving")) script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='正在保存文件…';e.hidden=false;}})()";
    else script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='保存失败，请重试';e.hidden=false;}})()";
    webkit_web_view_evaluate_javascript(s->web, script, -1, NULL, NULL, NULL, NULL, NULL);
}
static void download_finished(WebKitDownload *download, gpointer data) {
    if (!g_object_get_data(G_OBJECT(download), "yudesk-failed")) transfer_status(data, "complete");
    g_object_unref(download);
}
static void download_failed(WebKitDownload *download, GError *error, gpointer data) {
    g_object_set_data(G_OBJECT(download), "yudesk-failed", GINT_TO_POINTER(1));
    if (g_error_matches(error, WEBKIT_DOWNLOAD_ERROR, WEBKIT_DOWNLOAD_ERROR_CANCELLED_BY_USER)) transfer_status(data, "cancelled");
    else { transfer_status(data, "failed"); emit("error", "download_failed"); }
}
static gboolean download_destination(WebKitDownload *download, gchar *suggested, gpointer data) {
    Shell *s = data;
    WebKitURIResponse *response = webkit_download_get_response(download);
    if (!response || !permitted(s, webkit_uri_response_get_uri(response))) {
        webkit_download_cancel(download); return TRUE;
    }
#ifdef YUDESK_WINDOW_TEST
    if (s->test_download_destination) {
        gchar *path = s->test_download_destination; s->test_download_destination = NULL;
        if (!strcmp(path, "cancel")) webkit_download_cancel(download);
        else {
            gchar *uri = g_filename_to_uri(path, NULL, NULL);
            if (uri) { webkit_download_set_destination(download, uri); g_free(uri); }
            else webkit_download_cancel(download);
        }
        g_free(path); return TRUE;
    }
#endif
    // This is an OS file chooser, not an app confirmation dialog. Selecting a
    // destination and overwriting require explicit local user action.
    GtkFileChooserNative *chooser = gtk_file_chooser_native_new("保存文件", GTK_WINDOW(s->window),
        GTK_FILE_CHOOSER_ACTION_SAVE, "保存", "取消");
    if (s->file_dialog) { g_object_unref(chooser); webkit_download_cancel(download); return TRUE; }
    s->file_dialog = GTK_NATIVE_DIALOG(chooser);
    gchar *base = g_path_get_basename(suggested && *suggested ? suggested : "download");
    gtk_file_chooser_set_current_name(GTK_FILE_CHOOSER(chooser), base); g_free(base);
    gtk_file_chooser_set_do_overwrite_confirmation(GTK_FILE_CHOOSER(chooser), TRUE);
    gint result = gtk_native_dialog_run(GTK_NATIVE_DIALOG(chooser));
    s->file_dialog = NULL;
    gchar *uri = result == GTK_RESPONSE_ACCEPT ? gtk_file_chooser_get_uri(GTK_FILE_CHOOSER(chooser)) : NULL;
    if (uri) { transfer_status(s, "saving"); webkit_download_set_allow_overwrite(download, TRUE); webkit_download_set_destination(download, uri); g_free(uri); }
    else { transfer_status(s, "cancelled"); webkit_download_cancel(download); }
    g_object_unref(chooser); return TRUE;
}
static void download_started(WebKitWebContext *context, WebKitDownload *download, gpointer data) {
    (void)context;
    Shell *s = data;
    const char *uri = webkit_uri_request_get_uri(webkit_download_get_request(download));
    if (!permitted(s, uri)) { webkit_download_cancel(download); return; }
    g_object_ref(download);
    g_signal_connect(download, "decide-destination", G_CALLBACK(download_destination), s);
    g_signal_connect(download, "failed", G_CALLBACK(download_failed), s);
    g_signal_connect(download, "finished", G_CALLBACK(download_finished), s);
}

static const char drag_script[] =
// A WM move grab can consume button-up before WebKit sees it. WebKitGTK then
// still emits mousedown on the next physical press but may omit pointerdown.
// Use the trusted mouse event, paired with the native press/release guard.
"(()=>{document.addEventListener('mousedown',e=>{"
"if(!e.isTrusted||e.button!==0||!(e.target instanceof Element))return;"
"if(!e.target.closest('[data-window-drag]')||e.target.closest('button,a,input,label,select,textarea,form,[contenteditable],[role=\"button\"]'))return;"
"window.webkit.messageHandlers.yudeskDrag.postMessage('drag');},true);"
// WebKitGTK 2.50.4 aborts on tested DOM fullscreen; custom enter/leave handlers
// do not prevent that failure.
// Native-window fullscreen keeps the toolbar usable and bypasses only that path.
// This isolated, main-frame handler never accepts synthetic page events.
"document.addEventListener('click',e=>{if(!e.isTrusted||!(e.target instanceof Element))return;"
"const b=e.target.closest('button#fullscreen,button#exitFullscreen');if(!b)return;"
"e.preventDefault();e.stopImmediatePropagation();window.webkit.messageHandlers.yudeskDrag.postMessage(b.id==='exitFullscreen'?'unfullscreen':'fullscreen');},true);"
"document.addEventListener('keydown',e=>{if(e.isTrusted&&e.key==='Escape'&&document.getElementById('fullscreen')?.getAttribute('aria-pressed')==='true'){"
"e.preventDefault();e.stopImmediatePropagation();window.webkit.messageHandlers.yudeskDrag.postMessage('unfullscreen');}},true);})();";

static gboolean open_window(Shell *s, const char *url) {
    WebKitUserContentManager *manager = webkit_user_content_manager_new();
    g_signal_connect(manager, "script-message-received::yudeskDrag", G_CALLBACK(drag_message), s);
    if (!webkit_user_content_manager_register_script_message_handler_in_world(manager, "yudeskDrag", DRAG_WORLD)) {
        g_object_unref(manager); emit("error", "bridge_unavailable"); return FALSE;
    }
    WebKitUserScript *script = webkit_user_script_new_for_world(drag_script, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME,
        WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, DRAG_WORLD, NULL, NULL);
    webkit_user_content_manager_add_script(manager, script); webkit_user_script_unref(script);
    s->context = webkit_web_context_new_ephemeral();
    webkit_web_context_set_sandbox_enabled(s->context, TRUE);
    s->web = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW, "web-context", s->context, "user-content-manager", manager, NULL));
    g_object_unref(manager);
    WebKitSettings *settings = webkit_web_view_get_settings(s->web);
    webkit_settings_set_enable_javascript(settings, TRUE);
    webkit_settings_set_javascript_can_open_windows_automatically(settings, FALSE);
    // FALSE still permits standards-based writeText in a genuine user gesture.
    // TRUE bypasses read permission and would expose the local clipboard.
    webkit_settings_set_javascript_can_access_clipboard(settings, FALSE);
    webkit_settings_set_media_playback_requires_user_gesture(settings, FALSE);
    webkit_settings_set_enable_developer_extras(settings, FALSE);
    // Working native fullscreen is provided above. Reject other DOM fullscreen
    // requests safely instead of entering the affected WebKit coroutine.
    webkit_settings_set_enable_fullscreen(settings, FALSE);
    webkit_settings_set_user_agent_with_application_details(settings, "YuDeskNative", "2.0.0");
    s->window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(s->window), "YuDesk");
    gtk_window_set_wmclass(GTK_WINDOW(s->window), "yudesk", "YuDesk");
    gtk_window_set_icon_name(GTK_WINDOW(s->window), "yudesk");
    gtk_window_set_default_size(GTK_WINDOW(s->window), 860, 600);
    gtk_widget_set_size_request(s->window, 720, 480);
    gtk_window_set_resizable(GTK_WINDOW(s->window), TRUE);
    gtk_window_set_decorated(GTK_WINDOW(s->window), FALSE);
    gtk_window_set_position(GTK_WINDOW(s->window), GTK_WIN_POS_CENTER);
    gtk_container_add(GTK_CONTAINER(s->window), GTK_WIDGET(s->web));
    g_signal_connect(s->window, "delete-event", G_CALLBACK(deleted), s);
    g_signal_connect(s->window, "window-state-event", G_CALLBACK(window_state), s);
    g_signal_connect(s->web, "button-press-event", G_CALLBACK(press), s);
    g_signal_connect(s->web, "button-release-event", G_CALLBACK(release), s);
    g_signal_connect(s->web, "key-press-event", G_CALLBACK(key_press), s);
    g_signal_connect(s->web, "decide-policy", G_CALLBACK(policy), s);
    g_signal_connect(s->web, "load-changed", G_CALLBACK(loaded), s);
    g_signal_connect(s->web, "load-failed", G_CALLBACK(load_failed), s);
    g_signal_connect(s->web, "web-process-terminated", G_CALLBACK(terminated), s);
    g_signal_connect(s->web, "script-dialog", G_CALLBACK(script_dialog), s);
    g_signal_connect(s->web, "permission-request", G_CALLBACK(permission), s);
    g_signal_connect(s->context, "download-started", G_CALLBACK(download_started), s);
    s->initial_url = g_strdup(url);
    show_window(s); webkit_web_view_load_uri(s->web, url);
    s->timeout_source = g_timeout_add_seconds(20, load_timeout, s);
    return TRUE;
}

static const char *string_member(JsonObject *object, const char *name) {
    JsonNode *node = json_object_get_member(object, name);
    return node && JSON_NODE_HOLDS_VALUE(node) && json_node_get_value_type(node) == G_TYPE_STRING ? json_node_get_string(node) : NULL;
}
#ifdef YUDESK_WINDOW_TEST
// These diagnostics do not exist in the production executable. They are used
// only on an isolated Xvfb display with synthetic, non-secret fixture pages.
static void test_evaluated(GObject *source, GAsyncResult *result, gpointer data) {
    (void)data;
    JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(source), result, NULL);
    gchar *json = value ? jsc_value_to_json(value, 0) : NULL;
    if (json && strlen(json) <= MAX_LINE) { printf("{\"event\":\"test_result\",\"value\":%s}\n", json); fflush(stdout); }
    else emit("error", "test_evaluate_failed");
    g_free(json); if (value) g_object_unref(value);
}
static void test_snapshot_saved(GObject *source, GAsyncResult *result, gpointer data) {
    gchar *path = data;
    cairo_surface_t *surface = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(source), result, NULL);
    gboolean ok = surface && cairo_surface_write_to_png(surface, path) == CAIRO_STATUS_SUCCESS;
    if (surface) cairo_surface_destroy(surface);
    g_free(path); emit(ok ? "test_snapshot_saved" : "error", ok ? NULL : "test_snapshot_failed");
}
static gboolean test_command(Shell *s, JsonObject *object, const char *action) {
    if (!g_str_has_prefix(action, "test-")) return FALSE;
    if (!s->web) { emit("error", "not_open"); return TRUE; }
    if (!strcmp(action, "test-state")) {
        GdkWindow *gdk = gtk_widget_get_window(s->window);
        GdkWindowState state = gdk ? gdk_window_get_state(gdk) : 0;
        gint w, h, x, y; gtk_window_get_size(GTK_WINDOW(s->window), &w, &h); gtk_window_get_position(GTK_WINDOW(s->window), &x, &y);
        printf("{\"event\":\"test_state\",\"visible\":%s,\"decorated\":%s,\"iconified\":%s,\"width\":%d,\"height\":%d,\"x\":%d,\"y\":%d}\n",
            gtk_widget_get_visible(s->window) ? "true" : "false", gtk_window_get_decorated(GTK_WINDOW(s->window)) ? "true" : "false",
            (state & GDK_WINDOW_STATE_ICONIFIED) ? "true" : "false", w, h, x, y); fflush(stdout);
    } else if (!strcmp(action, "test-close-native")) gtk_window_close(GTK_WINDOW(s->window));
    else if (!strcmp(action, "test-clipboard-contents")) {
        gchar *value = gtk_clipboard_wait_for_text(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD));
        gboolean ok = !g_strcmp0(value, "设备码：123456789\nPIN：123456"); g_free(value);
        printf("{\"event\":\"test_result\",\"value\":%s}\n", ok ? "true" : "false"); fflush(stdout);
    } else if (!strcmp(action, "test-clipboard-replace")) {
        gtk_clipboard_set_text(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), "unrelated fixture clipboard", -1);
    }
    else if (!strcmp(action, "test-next-download")) {
        const char *path = string_member(object, "url");
        g_free(s->test_download_destination); s->test_download_destination = g_strdup(path);
    }
    else if (!strcmp(action, "test-crash-web")) webkit_web_view_terminate_web_process(s->web);
    else if (!strcmp(action, "test-evaluate")) {
        const char *script = string_member(object, "url");
        if (!script) emit("error", "invalid_command");
        else webkit_web_view_evaluate_javascript(s->web, script, -1, NULL, NULL, NULL, test_evaluated, s);
    } else if (!strcmp(action, "test-transfer-complete")) transfer_status(s, "complete");
    else if (!strcmp(action, "test-transfer-cancelled")) transfer_status(s, "cancelled");
    else if (!strcmp(action, "test-transfer-failed")) transfer_status(s, "failed");
    else if (!strcmp(action, "test-snapshot")) {
        const char *path = string_member(object, "url");
        if (!path || !g_path_is_absolute(path)) emit("error", "invalid_command");
        else webkit_web_view_get_snapshot(s->web, WEBKIT_SNAPSHOT_REGION_VISIBLE, WEBKIT_SNAPSHOT_OPTIONS_NONE,
            NULL, test_snapshot_saved, g_strdup(path));
    } else emit("error", "invalid_command");
    return TRUE;
}
#endif
static void command(Shell *s, const guint8 *line, gsize length) {
    if (!length || memchr(line, 0, length) || !g_utf8_validate((const gchar *)line, length, NULL)) {
        emit("error", "invalid_command"); return;
    }
    JsonParser *parser = json_parser_new();
    if (!json_parser_load_from_data(parser, (const gchar *)line, length, NULL)) { emit("error", "invalid_command"); goto done; }
    JsonNode *root = json_parser_get_root(parser);
    if (!JSON_NODE_HOLDS_OBJECT(root)) { emit("error", "invalid_command"); goto done; }
    JsonObject *object = json_node_get_object(root);
    const char *action = string_member(object, "action");
#ifdef YUDESK_WINDOW_TEST
    if (action && test_command(s, object, action)) goto done;
#endif
    GList *members = json_object_get_members(object);
    gboolean valid = action != NULL;
    for (GList *item = members; item; item = item->next)
        if (strcmp(item->data, "action") && strcmp(item->data, "url")) valid = FALSE;
    g_list_free(members);
    if (!valid || (strcmp(action, "open") && json_object_has_member(object, "url"))) { emit("error", "invalid_command"); goto done; }
    if (!strcmp(action, "open")) {
        const char *url = string_member(object, "url");
        if (s->window) { emit("error", "already_open"); goto done; }
        gint port = local_port(url, TRUE, FALSE);
        if (port < 0) { emit("error", "invalid_url"); goto done; }
        s->port = port;
        if (!open_window(s, url)) stop_shell(s);
    } else if (!strcmp(action, "close")) stop_shell(s);
    else if (!strcmp(action, "show") || !strcmp(action, "hide") || !strcmp(action, "minimize")) {
        if (!s->window) { emit("error", "not_open"); goto done; }
        if (!strcmp(action, "show")) show_window(s);
        else if (!strcmp(action, "hide")) { gtk_widget_hide(s->window); gtk_window_set_skip_taskbar_hint(GTK_WINDOW(s->window), TRUE); }
        else gtk_window_iconify(GTK_WINDOW(s->window));
    } else emit("error", "invalid_command");
done:
    g_object_unref(parser);
}

static gboolean input_ready(GIOChannel *channel, GIOCondition condition, gpointer data) {
    (void)channel;
    Shell *s = data;
    if (condition & (G_IO_ERR | G_IO_NVAL)) { emit("error", "ipc_read_failed"); stop_shell(s); return G_SOURCE_REMOVE; }
    guint8 bytes[4096];
    // Bound each main-loop turn, including many tiny commands. No unbounded queue.
    for (guint read_count = 0; read_count < 8; ++read_count) {
        ssize_t n = read(STDIN_FILENO, bytes, sizeof(bytes));
        if (n == 0) { stop_shell(s); return G_SOURCE_REMOVE; }
        if (n < 0) {
            if (errno == EINTR) continue;
            if (errno == EAGAIN || errno == EWOULDBLOCK) return G_SOURCE_CONTINUE;
            emit("error", "ipc_read_failed"); stop_shell(s); return G_SOURCE_REMOVE;
        }
        for (ssize_t i = 0; i < n; ++i) {
            if (bytes[i] == '\n') {
                command(s, s->pending->data, s->pending->len); g_byte_array_set_size(s->pending, 0);
                if (s->closing) return G_SOURCE_REMOVE;
            } else {
                if (s->pending->len >= MAX_LINE) { emit("error", "ipc_line_too_long"); stop_shell(s); return G_SOURCE_REMOVE; }
                g_byte_array_append(s->pending, bytes + i, 1);
            }
        }
    }
    return G_SOURCE_CONTINUE;
}

static gboolean self_test(void) {
    const char *bad[] = {"http://localhost:8234/?access_token=x", "https://127.0.0.1:8234/?access_token=x",
        "http://127.0.0.1:0/?access_token=x", "http://127.0.0.1:65536/?access_token=x",
        "http://127.0.0.1:8234/", "http://127.0.0.1:8234/?access_token=",
        "http://127.0.0.1:8234/?access_token=x&access_token=y", "http://127.0.0.1:8234/?access_token=%00",
        "http://127.0.0.1:8234/?access_token=x#secret", "http://127.0.0.1:8234@evil.test/?access_token=x",
        "http://127.0.0.1:8234/\n?access_token=x"};
    for (guint i = 0; i < G_N_ELEMENTS(bad); ++i) if (local_port(bad[i], TRUE, FALSE) >= 0) return FALSE;
    Shell s = {.port = 8234};
    return local_port("http://127.0.0.1:8234/?access_token=test", TRUE, FALSE) == 8234 &&
        permitted(&s, "http://127.0.0.1:8234/api/state") && !permitted(&s, "http://127.0.0.1:8235/") &&
        !permitted(&s, "file:///etc/passwd");
}

int main(int argc, char **argv) {
    signal(SIGPIPE, SIG_IGN);
    if (argc == 2 && !strcmp(argv[1], "--self-test")) {
        gboolean ok = self_test(); emit(ok ? "self_test_passed" : "error", ok ? NULL : "self_test_failed"); return ok ? 0 : 1;
    }
    if (argc != 1) { emit("error", "invalid_arguments"); return 2; }
    if (!gtk_init_check(&argc, &argv)) { emit("error", "display_unavailable"); return 1; }
    Shell shell = {0}; shell.pending = g_byte_array_sized_new(4096);
    gint flags = fcntl(STDIN_FILENO, F_GETFL);
    if (flags < 0 || fcntl(STDIN_FILENO, F_SETFL, flags | O_NONBLOCK) < 0) { emit("error", "ipc_read_failed"); return 1; }
    GIOChannel *input = g_io_channel_unix_new(STDIN_FILENO);
    g_io_channel_set_encoding(input, NULL, NULL); g_io_channel_set_buffered(input, FALSE);
    guint watch = g_io_add_watch(input, G_IO_IN | G_IO_HUP | G_IO_ERR | G_IO_NVAL, input_ready, &shell);
    gtk_main();
    // The watch may already have been removed by EOF, so look it up first.
    if (g_main_context_find_source_by_id(NULL, watch)) g_source_remove(watch);
    if (shell.reload_source) g_source_remove(shell.reload_source);
    if (shell.timeout_source) g_source_remove(shell.timeout_source);
    if (shell.window) gtk_widget_destroy(shell.window);
    if (shell.context) g_object_unref(shell.context);
    g_io_channel_unref(input); g_byte_array_unref(shell.pending); g_free(shell.initial_url);
    return 0;
}
