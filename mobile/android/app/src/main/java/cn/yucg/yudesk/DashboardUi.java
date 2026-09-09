package cn.yucg.yudesk;

import android.app.Activity;
import android.app.Dialog;
import android.content.ClipData;
import android.content.ClipDescription;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.ColorFilter;
import android.graphics.Paint;
import android.graphics.Path;
import android.graphics.PixelFormat;
import android.graphics.RectF;
import android.graphics.Typeface;
import android.graphics.drawable.Drawable;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.graphics.drawable.StateListDrawable;
import android.os.Build;
import android.os.PersistableBundle;
import android.text.Editable;
import android.text.InputFilter;
import android.text.InputType;
import android.text.TextUtils;
import android.text.TextWatcher;
import android.text.method.PasswordTransformationMethod;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.view.inputmethod.EditorInfo;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.ImageButton;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.RadioButton;
import android.widget.RadioGroup;
import android.widget.ScrollView;
import android.widget.TextView;
import android.os.SystemClock;
import org.json.JSONArray;
import org.json.JSONObject;

/** Native dashboard presentation. Session, permission and engine ownership stay in the activity. */
final class DashboardUi {
    static final int BLUE = 0xff1769e8;
    private static final int INK = 0xff182b46, MUTED = 0xff63738a;
    private static final int BACKGROUND = 0xfff4f7fb, FIELD = 0xfff0f4fa;
    private static final int LINE = 0xffe2e9f2, PALE_BLUE = 0xffeaf2ff;
    private static final int GREEN = 0xff18755b, AMBER = 0xff956019, RED = 0xffb84046;
    private static final Typeface MEDIUM = Typeface.create("sans-serif-medium", Typeface.NORMAL);

    interface Actions {
        void connect();
        void startSharing();
        void stopSharing();
        void startMeeting();
        void endMeeting();
        void joinMeeting();
        void accessibilitySettings();
        void rotatePin(Runnable finished);
        void clearHistory();
        void exit();
    }

    final ScrollView root;
    final EditText code, pin, meetingCode;
    // Keep the activity's existing connect() contract; the visible selector uses native radio buttons.
    final CheckBox viewOnly;
    final Button connect, joinMeeting;
    final LinearLayout history;
    final TextView identity, state;
    private final Activity activity;
    private final Actions actions;
    private final TextView localPin, status, pinNote, sharingStatus, touchStatus, meetingNumber, meetingStatus, meetingNote;
    private final ImageButton revealPin, rotatePin, copyCode, copyPin, revealInput, copyMeeting;
    private final Button share, accessibility, meetingAction;
    private String deviceCode = "", secret = "";
    private boolean pinVisible, inputVisible, sharing, meetingActive, rotating, historyExpanded, connectionPending;
    private JSONArray recent = new JSONArray();
    private JSONObject lastState = new JSONObject();
    private Dialog details;
    private TextView detailState;
    private long copiedUntil;
    private String copiedMessage="";

    DashboardUi(Activity activity, Actions actions) {
        this.activity = activity;
        this.actions = actions;
        root = new ScrollView(activity);
        root.setFillViewport(true);
        root.setBackgroundColor(BACKGROUND);
        root.setVerticalScrollBarEnabled(false);
        root.setClipToPadding(false);
        LinearLayout center = column();
        center.setGravity(Gravity.CENTER_HORIZONTAL);
        root.addView(center, new ScrollView.LayoutParams(-1, -2));
        LinearLayout page = new LinearLayout(activity) {
            @Override protected void onMeasure(int width, int height) {
                super.onMeasure(MeasureSpec.makeMeasureSpec(Math.min(MeasureSpec.getSize(width), dp(560)), MeasureSpec.EXACTLY), height);
            }
        };
        page.setOrientation(LinearLayout.VERTICAL);
        page.setPadding(dp(16), dp(10), dp(16), dp(20));
        center.addView(page, new LinearLayout.LayoutParams(-1, -2));

        LinearLayout header = row();
        ImageView mark = new ImageView(activity);
        mark.setImageResource(R.drawable.ic_yu);
        mark.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        header.addView(mark, new LinearLayout.LayoutParams(dp(36), dp(36)));
        TextView brand = label("YuDesk", 23, INK);
        brand.setTypeface(MEDIUM);
        brand.setSingleLine(true);
        brand.setAutoSizeTextTypeUniformWithConfiguration(12, 23, 1, TypedValue.COMPLEX_UNIT_SP);
        LinearLayout.LayoutParams brandParams = new LinearLayout.LayoutParams(0, dp(40), 1);
        brandParams.setMarginStart(dp(10));
        header.addView(brand, brandParams);
        TextView preview = label("预览版", 10, BLUE);
        preview.setPadding(dp(7), dp(3), dp(7), dp(3));
        preview.setBackground(shape(activity, PALE_BLUE, 6, Color.TRANSPARENT));
        LinearLayout.LayoutParams previewParams = new LinearLayout.LayoutParams(-2, -2);
        previewParams.setMarginStart(dp(8));
        header.addView(preview, previewParams);
        ImageButton exit = iconButton(Icon.POWER, "退出应用并停止共享", actions::exit);
        header.addView(exit, square(48));
        page.addView(header);
        space(page, 12);

        LinearLayout local = card(page, PALE_BLUE, 0xffdce8fc);
        LinearLayout localHeading = row();
        TextView localTitle = heading("此设备 · 设备码", 12);
        localTitle.setTextColor(MUTED);
        localHeading.addView(localTitle, new LinearLayout.LayoutParams(0, -2, 1));
        status = label("连接中", 11, MUTED);
        status.setTypeface(MEDIUM);
        localHeading.addView(status);
        local.addView(localHeading);

        LinearLayout identityRow = row();
        identity = label("正在登记…", 26, INK);
        identity.setTypeface(Typeface.create("monospace", Typeface.NORMAL));
        identity.setSingleLine(true);
        identity.setAutoSizeTextTypeUniformWithConfiguration(12, 26, 1, TypedValue.COMPLEX_UNIT_SP);
        identity.setTextIsSelectable(true);
        identity.setTextDirection(View.TEXT_DIRECTION_LTR);
        identityRow.addView(identity, new LinearLayout.LayoutParams(0, dp(48), 1));
        copyCode = iconButton(Icon.COPY, "复制本机设备码和 PIN，仅分享给信任的人", () -> copy(deviceCode + "  " + secret, true));
        identityRow.addView(copyCode, square(48));
        local.addView(identityRow);
        divider(local, 0xffd5e2f7);

        LinearLayout pinRow = row();
        LinearLayout pinValues = column();
        pinValues.setGravity(Gravity.CENTER_VERTICAL);
        pinValues.addView(label("PIN", 10, MUTED));
        localPin = label("••• •••", 17, INK);
        localPin.setTypeface(Typeface.MONOSPACE);
        localPin.setSingleLine(true);
        localPin.setTextDirection(View.TEXT_DIRECTION_LTR);
        localPin.setAutoSizeTextTypeUniformWithConfiguration(13, 17, 1, TypedValue.COMPLEX_UNIT_SP);
        pinValues.addView(localPin, new LinearLayout.LayoutParams(-1, -2));
        pinRow.addView(pinValues, new LinearLayout.LayoutParams(0, -2, 1));
        revealPin = iconButton(Icon.EYE, "显示本机 PIN", () -> {
            pinVisible = !pinVisible;
            renderPin();
        });
        rotatePin = iconButton(Icon.ROTATE, "更换本机 PIN", () -> {
            rotating = true;
            pinVisible = false;
            renderPin();
            actions.rotatePin(() -> {
                rotating = false;
                renderPin();
            });
        });
        copyPin = iconButton(Icon.COPY, "复制本机 PIN，仅分享给信任的人", () -> copy(secret, true));
        pinRow.addView(revealPin, square(48));
        pinRow.addView(rotatePin, square(48));
        pinRow.addView(copyPin, square(48));
        local.addView(pinRow);
        pinNote = label("仅向信任的人分享设备码和 PIN", 11, MUTED);
        local.addView(pinNote);
        state = label("连接加密管理通道…", 12, MUTED);
        state.setPadding(0, dp(6), 0, 0);
        local.addView(state);

        LinearLayout target = card(page, Color.WHITE, 0xffdce6f5);
        LinearLayout targetHeading = row();
        targetHeading.addView(decorativeIcon(Icon.MONITOR, BLUE), square(20));
        TextView targetTitle = heading("连接远程设备", 17);
        LinearLayout.LayoutParams titleParams = new LinearLayout.LayoutParams(-1, -2);
        titleParams.setMarginStart(dp(8));
        targetHeading.addView(targetTitle, titleParams);
        target.addView(targetHeading);
        space(target, 14);

        TextView codeLabel = label("设备码", 12, MUTED);
        target.addView(codeLabel);
        space(target, 6);
        code = input("输入 9 位设备码", 9, false);
        code.setId(View.generateViewId());
        codeLabel.setLabelFor(code.getId());
        code.setBackground(fieldBackground(activity));
        code.setImeOptions(EditorInfo.IME_ACTION_NEXT | EditorInfo.IME_FLAG_NO_EXTRACT_UI);
        target.addView(code, new LinearLayout.LayoutParams(-1, -2));
        space(target, 8);
        LinearLayout pinField = row();
        pinField.setAddStatesFromChildren(true);
        pinField.setBackground(fieldBackground(activity));
        pin = input("PIN（可选）", 6, true);
        pin.setId(View.generateViewId());
        pin.setContentDescription("远程设备 PIN，可留空等待对方同意");
        pin.setImeOptions(EditorInfo.IME_ACTION_DONE | EditorInfo.IME_FLAG_NO_EXTRACT_UI);
        pinField.addView(pin, new LinearLayout.LayoutParams(0, -2, 1));
        revealInput = iconButton(Icon.EYE, "显示输入的 PIN", () -> {
            inputVisible = !inputVisible;
            renderInputVisibility();
        });
        pinField.addView(revealInput, square(48));
        target.addView(pinField, new LinearLayout.LayoutParams(-1, -2));
        code.setNextFocusForwardId(pin.getId());
        space(target, 10);

        viewOnly = new CheckBox(activity);
        RadioGroup modes = new RadioGroup(activity);
        modes.setOrientation(LinearLayout.HORIZONTAL);
        modes.setPadding(dp(3), dp(3), dp(3), dp(3));
        modes.setBackground(shape(activity, FIELD, 12, Color.TRANSPARENT));
        RadioButton control = mode("远程控制");
        RadioButton watch = mode("仅观看");
        modes.addView(control, new RadioGroup.LayoutParams(0, -2, 1));
        modes.addView(watch, new RadioGroup.LayoutParams(0, -2, 1));
        modes.check(control.getId());
        modes.setOnCheckedChangeListener((group, checkedId) -> viewOnly.setChecked(checkedId == watch.getId()));
        target.addView(modes, new LinearLayout.LayoutParams(-1, -2));
        space(target, 10);
        connect = button(activity, "连接设备", true, actions::connect);
        connect.setCompoundDrawablesRelativeWithIntrinsicBounds(null, null, new Icon(activity, Icon.ARROW, Color.WHITE), null);
        target.addView(connect, new LinearLayout.LayoutParams(-1, -2));
        pin.setOnEditorActionListener((view, actionId, event) -> {
            if (actionId != EditorInfo.IME_ACTION_DONE) return false;
            if (connect.isEnabled()) connect.performClick();
            return true;
        });
        TextView connectionNote = label("PIN 留空需对方确认，最长等待 60 秒", 11, MUTED);
        connectionNote.setPadding(0, dp(8), 0, 0);
        target.addView(connectionNote);

        LinearLayout meetingCard = card(page, Color.WHITE, 0xffdce6f5);
        LinearLayout meetingHeading = row();
        meetingHeading.addView(decorativeIcon(Icon.MONITOR, BLUE), square(20));
        TextView meetingTitle = heading("会议", 17);
        LinearLayout.LayoutParams meetingTitleParams = new LinearLayout.LayoutParams(0, -2, 1);
        meetingTitleParams.setMarginStart(dp(8));
        meetingHeading.addView(meetingTitle, meetingTitleParams);
        meetingStatus = label("未开始", 11, MUTED);
        meetingStatus.setTypeface(MEDIUM);
        meetingHeading.addView(meetingStatus);
        meetingCard.addView(meetingHeading);
        space(meetingCard, 10);
        TextView hostLabel = label("主持人共享本机屏幕", 12, MUTED);
        meetingCard.addView(hostLabel);
        LinearLayout hostRow = row();
        meetingNumber = label("发起后生成 9 位会议号", 18, INK);
        meetingNumber.setTypeface(Typeface.MONOSPACE);
        meetingNumber.setSingleLine(true);
        meetingNumber.setTextDirection(View.TEXT_DIRECTION_LTR);
        meetingNumber.setAutoSizeTextTypeUniformWithConfiguration(11, 18, 1, TypedValue.COMPLEX_UNIT_SP);
        hostRow.addView(meetingNumber, new LinearLayout.LayoutParams(0, dp(48), 1));
        copyMeeting = iconButton(Icon.COPY, "复制会议号", this::copyMeetingCode);
        hostRow.addView(copyMeeting, square(48));
        meetingAction = button(activity, "发起会议", false, () -> {
            if (meetingActive) actions.endMeeting(); else actions.startMeeting();
        });
        hostRow.addView(meetingAction, new LinearLayout.LayoutParams(-2, -2));
        meetingCard.addView(hostRow);
        divider(meetingCard, LINE);
        space(meetingCard, 10);
        TextView joinLabel = label("加入会议 · 无需主持人确认", 12, MUTED);
        meetingCard.addView(joinLabel);
        space(meetingCard, 6);
        LinearLayout joinRow = row();
        meetingCode = input("输入 9 位会议号", 9, false);
        meetingCode.setId(View.generateViewId());
        meetingCode.setBackground(fieldBackground(activity));
        meetingCode.setImeOptions(EditorInfo.IME_ACTION_DONE | EditorInfo.IME_FLAG_NO_EXTRACT_UI);
        LinearLayout.LayoutParams meetingCodeParams = new LinearLayout.LayoutParams(0, -2, 1);
        meetingCodeParams.setMarginEnd(dp(8));
        joinRow.addView(meetingCode, meetingCodeParams);
        joinMeeting = button(activity, "加入", true, actions::joinMeeting);
        joinRow.addView(joinMeeting, new LinearLayout.LayoutParams(dp(94), -2));
        meetingCard.addView(joinRow);
        meetingNote = label("参会端仅观看；Android 暂不支持系统声音。", 11, MUTED);
        meetingNote.setPadding(0, dp(8), 0, 0);
        meetingCard.addView(meetingNote);
        meetingCode.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence value, int start, int count, int after) { }
            @Override public void onTextChanged(CharSequence value, int start, int before, int count) { updateMeetingJoin(); }
            @Override public void afterTextChanged(Editable value) { }
        });
        meetingCode.setOnEditorActionListener((view, actionId, event) -> {
            if (actionId != EditorInfo.IME_ACTION_DONE) return false;
            if (joinMeeting.isEnabled()) joinMeeting.performClick();
            return true;
        });

        LinearLayout receive = card(page, Color.WHITE, LINE);
        receive.addView(heading("让对方连接此设备", 13));
        space(receive, 5);
        LinearLayout shareRow = row();
        shareRow.addView(decorativeIcon(Icon.MONITOR, BLUE), square(20));
        LinearLayout shareLabels = rowLabels(shareRow, "屏幕共享");
        sharingStatus = label("未开启", 11, MUTED);
        shareLabels.addView(sharingStatus);
        share = button(activity, "授权共享", false, () -> {
            if (sharing) actions.stopSharing(); else actions.startSharing();
        });
        shareRow.addView(share, new LinearLayout.LayoutParams(-2, -2));
        receive.addView(shareRow);
        space(receive, 4);
        divider(receive, LINE);
        space(receive, 4);
        LinearLayout touchRow = row();
        touchRow.addView(decorativeIcon(Icon.TOUCH, BLUE), square(20));
        LinearLayout touchLabels = rowLabels(touchRow, "远程触控");
        touchStatus = label("仅观看无需开启", 11, MUTED);
        touchLabels.addView(touchStatus);
        accessibility = button(activity, "去开启", false, actions::accessibilitySettings);
        touchRow.addView(accessibility, new LinearLayout.LayoutParams(-2, -2));
        receive.addView(touchRow);
        TextView consent = label("每次共享均需系统授权，可随时从通知停止。", 11, MUTED);
        consent.setPadding(0, dp(9), 0, 0);
        receive.addView(consent);

        history = column();
        page.addView(history, new LinearLayout.LayoutParams(-1, -2));
        space(page, 8);
        Button settings = button(activity, "设置与能力", false, this::showDetails);
        quiet(settings);
        settings.setGravity(Gravity.START | Gravity.CENTER_VERTICAL);
        settings.setCompoundDrawablesRelativeWithIntrinsicBounds(new Icon(activity, Icon.SETTINGS, MUTED), null, new Icon(activity, Icon.CHEVRON, MUTED), null);
        page.addView(settings, new LinearLayout.LayoutParams(-1, -2));
        TextView version = label(version() + " · Android", 10, MUTED);
        version.setGravity(Gravity.CENTER);
        version.setPadding(0, dp(8), 0, 0);
        page.addView(version);
        page.setFocusableInTouchMode(true);
        page.requestFocus();
        root.addOnAttachStateChangeListener(new View.OnAttachStateChangeListener() {
            @Override public void onViewAttachedToWindow(View v) { }
            @Override public void onViewDetachedFromWindow(View v) {
                pinVisible = false;
                inputVisible = false;
                renderPin();
                renderInputVisibility();
                if (details != null) details.dismiss();
            }
        });
        update(new JSONObject());
    }

    void update(JSONObject value) {
        if (value.length() == 0) {
            // Preserve a working Stop action if a transient read fails while sharing.
            setText(state, "正在读取设备状态…");
            state.setVisibility(View.VISIBLE);
            setText(status, "读取中");
            status.setTextColor(MUTED);
            status.setCompoundDrawablesRelativeWithIntrinsicBounds(new Icon(activity, Icon.DOT, MUTED), null, null, null);
            enable(copyCode, deviceCode.matches("[1-9][0-9]{8}") && secret.matches("[0-9]{6}") && !rotating);
            renderPin();
            return;
        }
        lastState = value;
        deviceCode = value.optString("deviceCode");
        String nextPin = value.optString("pin");
        if (!secret.equals(nextPin)) pinVisible = false;
        secret = nextPin;
        boolean running = value.optBoolean("running", true);
        boolean online = value.optBoolean("online");
        boolean active = value.optBoolean("active");
        boolean connected = value.optBoolean("connected");
        boolean nextSharing = value.optBoolean("sharing");
        boolean touch = value.optBoolean("accessibility");
        String nextMeetingCode = value.optString("meetingCode");
        boolean nextMeeting = value.optBoolean("meeting") && nextMeetingCode.matches("[1-9][0-9]{8}");
        setText(identity, deviceCode.isEmpty() ? "正在登记…" : groupCode(deviceCode));
        identity.setContentDescription(deviceCode.isEmpty() ? "正在登记设备码" : "本机设备码 " + groupCode(deviceCode));
        enable(copyCode, deviceCode.matches("[1-9][0-9]{8}") && secret.matches("[0-9]{6}") && !rotating);
        String statusText = !running ? "已停止" : !online ? (deviceCode.isEmpty() ? "连接中" : "连接中断") : !active ? "待激活" : connected ? "对方已连接" : "已就绪";
        int statusColor = !running || (!online && !deviceCode.isEmpty()) ? AMBER : online && active ? GREEN : MUTED;
        if (!statusText.contentEquals(status.getText())) {
            status.setText(statusText);
            status.setTextColor(statusColor);
            status.setCompoundDrawablesRelativeWithIntrinsicBounds(new Icon(activity, Icon.DOT, statusColor), null, null, null);
            status.setCompoundDrawablePadding(dp(2));
        }
        String message = value.optString("message");
        // Keep exceptional engine messages visible; routine readiness is already represented by the badge.
        boolean routine = online && active && message.equals("管理通道已验证");
        state.setVisibility(routine || message.isEmpty() ? View.GONE : View.VISIBLE);
        setText(state, message);
        boolean copied=SystemClock.elapsedRealtime()<copiedUntil;
        setText(pinNote, copied?copiedMessage:value.optBoolean("pinSynced") ? "仅向信任的人分享设备码和 PIN" : "PIN 尚未同步 · 仅向信任的人分享");
        pinNote.setTextColor(copied?BLUE:MUTED);
        renderPin();
        setText(sharingStatus, nextSharing ? (connected ? "共享中 · 对方已连接" : "共享中 · 等待连接") : "未开启");
        sharingStatus.setTextColor(nextSharing ? GREEN : MUTED);
        if (sharing != nextSharing || share.getTag() == null) {
            sharing = nextSharing;
            share.setTag(sharing);
            share.setText(sharing ? "停止共享" : "授权共享");
            share.setTextColor(sharing ? RED : BLUE);
            share.setBackground(ripple(activity, sharing ? 0xfffff0f0 : PALE_BLUE, 10, Color.TRANSPARENT));
            share.setContentDescription(sharing ? "停止屏幕共享并结束接收连接" : "授权共享屏幕，需要系统确认");
        }
        setText(touchStatus, touch ? "已开启 · 可在系统撤销" : "仅观看无需开启");
        setText(accessibility, touch ? "管理" : "去开启");
        accessibility.setContentDescription(touch ? "管理远程触控无障碍权限" : "了解并开启远程触控无障碍权限");
        if (meetingActive != nextMeeting || meetingAction.getTag() == null) {
            meetingActive = nextMeeting;
            meetingAction.setTag(nextMeeting);
            setText(meetingAction, nextMeeting ? "结束会议" : "发起会议");
            meetingAction.setTextColor(nextMeeting ? RED : BLUE);
            meetingAction.setBackground(ripple(activity, nextMeeting ? 0xfffff0f0 : PALE_BLUE, 10, Color.TRANSPARENT));
        }
        setText(meetingNumber, nextMeeting ? groupCode(nextMeetingCode) : "发起后生成 9 位会议号");
        meetingNumber.setContentDescription(nextMeeting ? "当前会议号 " + groupCode(nextMeetingCode) : "当前没有会议");
        setText(meetingStatus, nextMeeting ? (connected ? "参会中" : "等待加入") : "未开始");
        meetingStatus.setTextColor(nextMeeting ? GREEN : MUTED);
        enable(meetingAction, !connectionPending && running && online && active && (!connected || nextMeeting));
        enable(copyMeeting, nextMeeting);
        setText(meetingNote, SystemClock.elapsedRealtime() < copiedUntil && copiedMessage.startsWith("会议号") ? copiedMessage : "参会端仅观看；Android 暂不支持系统声音。");
        meetingNote.setTextColor(SystemClock.elapsedRealtime() < copiedUntil && copiedMessage.startsWith("会议号") ? BLUE : MUTED);
        updateMeetingJoin();
        if (detailState != null && details != null && details.isShowing()) setText(detailState, detailedState());
    }

    void setConnectionPending(boolean pending) {
        connectionPending = pending;
        enable(connect, !pending);
        updateMeetingJoin();
    }

    private void updateMeetingJoin() {
        boolean available = lastState.optBoolean("running", true) && lastState.optBoolean("online") && lastState.optBoolean("active");
        enable(joinMeeting, !connectionPending && available && meetingCode.getText().toString().replace(" ", "").matches("[1-9][0-9]{8}"));
    }

    void renderHistory(JSONArray devices) {
        recent = devices;
        history.removeAllViews();
        LinearLayout header = row();
        header.setMinimumHeight(dp(devices.length() == 0 ? 28 : 48));
        header.addView(heading("最近设备", 13), new LinearLayout.LayoutParams(0, -2, 1));
        if (devices.length() > 0) {
            Button clear = button(activity, "清空", false, actions::clearHistory);
            quiet(clear);
            clear.setContentDescription("清空最近设备记录");
            header.addView(clear, new LinearLayout.LayoutParams(-2, -2));
        }
        history.addView(header);
        if (devices.length() == 0) {
            TextView empty = label("连接成功后显示设备码，不保存 PIN。", 12, MUTED);
            empty.setPadding(0, dp(4), 0, dp(8));
            history.addView(empty);
            return;
        }
        LinearLayout rows = column();
        rows.setBackground(shape(activity, Color.WHITE, 14, LINE));
        int count = historyExpanded ? devices.length() : Math.min(3, devices.length());
        for (int i = 0; i < count; i++) {
            String id = devices.optString(i);
            Button item = button(activity, groupCode(id), false, () -> {
                code.setText(id);
                code.setSelection(code.length());
                pin.setText("");
                inputVisible = false;
                renderInputVisibility();
                code.requestFocus();
                code.requestRectangleOnScreen(new android.graphics.Rect(0, 0, code.getWidth(), code.getHeight()), false);
                code.announceForAccessibility("已填入设备码 " + groupCode(id) + "，PIN 已清空");
            });
            quiet(item);
            item.setTextColor(INK);
            item.setTextSize(16);
            item.setTypeface(Typeface.MONOSPACE);
            item.setGravity(Gravity.START | Gravity.CENTER_VERTICAL);
            item.setTextDirection(View.TEXT_DIRECTION_LTR);
            item.setContentDescription("填入设备码 " + groupCode(id));
            item.setCompoundDrawablesRelativeWithIntrinsicBounds(new Icon(activity, Icon.MONITOR, MUTED), null, new Icon(activity, Icon.ARROW, MUTED), null);
            rows.addView(item, new LinearLayout.LayoutParams(-1, -2));
            if (i + 1 < count) divider(rows, LINE);
        }
        history.addView(rows, new LinearLayout.LayoutParams(-1, -2));
        if (devices.length() > 3) {
            Button more = button(activity, historyExpanded ? "收起记录" : "查看其余 " + (devices.length() - 3) + " 台设备", false, () -> {
                historyExpanded = !historyExpanded;
                renderHistory(recent);
            });
            quiet(more);
            history.addView(more, new LinearLayout.LayoutParams(-1, -2));
        }
    }

    private void renderPin() {
        boolean valid = secret.matches("[0-9]{6}");
        setText(localPin, pinVisible && valid ? groupCode(secret) : "••• •••");
        localPin.setContentDescription(pinVisible && valid ? "本机 PIN " + groupCode(secret) : "本机 PIN 已隐藏");
        if (revealPin.getTag() == null || !revealPin.getTag().equals(pinVisible)) {
            revealPin.setTag(pinVisible);
            revealPin.setImageDrawable(new Icon(activity, pinVisible ? Icon.EYE_OFF : Icon.EYE, BLUE));
        }
        describe(revealPin, pinVisible ? "隐藏本机 PIN" : "显示本机 PIN");
        enable(revealPin, valid && !rotating);
        enable(copyCode, deviceCode.matches("[1-9][0-9]{8}") && valid && !rotating);
        enable(copyPin, valid && !rotating);
        enable(rotatePin, valid && !rotating);
        describe(rotatePin, rotating ? "正在更换 PIN" : "更换本机 PIN");
    }

    private void renderInputVisibility() {
        int selectionStart = pin.getSelectionStart(), selectionEnd = pin.getSelectionEnd();
        pin.setTransformationMethod(inputVisible ? null : PasswordTransformationMethod.getInstance());
        pin.setSelection(Math.max(0, selectionStart), Math.max(0, selectionEnd));
        revealInput.setImageDrawable(new Icon(activity, inputVisible ? Icon.EYE_OFF : Icon.EYE, BLUE));
        describe(revealInput, inputVisible ? "隐藏输入的 PIN" : "显示输入的 PIN");
    }

    private void copy(String value, boolean sensitive) {
        if (value.isEmpty()) return;
        ClipboardManager clipboard = (ClipboardManager) activity.getSystemService(Context.CLIPBOARD_SERVICE);
        boolean includesDevice = sensitive && value.length() > 6;
        ClipData clip = ClipData.newPlainText(includesDevice ? "YuDesk 设备码与 PIN" : sensitive ? "YuDesk PIN" : "YuDesk 设备码", value);
        if (sensitive && Build.VERSION.SDK_INT >= 33) {
            PersistableBundle extras = new PersistableBundle();
            extras.putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true);
            clip.getDescription().setExtras(extras);
        }
        clipboard.setPrimaryClip(clip);
        copiedMessage=includesDevice?"设备码和 PIN 已复制":sensitive?"PIN 已复制，仅分享给信任的人":"设备码已复制";
        copiedUntil=SystemClock.elapsedRealtime()+2500;pinNote.setText(copiedMessage);pinNote.setTextColor(BLUE);pinNote.announceForAccessibility(copiedMessage);
    }

    private void copyMeetingCode() {
        String value = lastState.optString("meetingCode");
        if (!value.matches("[1-9][0-9]{8}")) return;
        ClipboardManager clipboard = (ClipboardManager) activity.getSystemService(Context.CLIPBOARD_SERVICE);
        ClipData clip = ClipData.newPlainText("YuDesk 临时会议号", value);
        if (Build.VERSION.SDK_INT >= 33) {
            PersistableBundle extras = new PersistableBundle();
            extras.putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true);
            clip.getDescription().setExtras(extras);
        }
        clipboard.setPrimaryClip(clip);
        copiedMessage = "会议号已复制，2 小时内有效";
        copiedUntil = SystemClock.elapsedRealtime() + 2500;
        setText(meetingNote, copiedMessage);
        meetingNote.setTextColor(BLUE);
        meetingNote.announceForAccessibility(copiedMessage);
    }

    private String detailedState() {
        String message = lastState.optString("message", "正在读取设备状态…");
        if (lastState.optBoolean("online") && !lastState.optBoolean("active")) message += "\n设备未激活，请联系网站管理员。";
        return message + "\n" + (lastState.optBoolean("sharing") ? "屏幕共享已开启" : "屏幕未共享") + " · " + (lastState.optBoolean("accessibility") ? "触控权限已开启" : "触控权限未开启");
    }

    private void showDetails() {
        if (details != null && details.isShowing()) return;
        Dialog dialog = new Dialog(activity);
        details = dialog;
        dialog.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout box = column();
        box.setPadding(dp(20), dp(12), dp(20), dp(16));
        box.setBackground(shape(activity, Color.WHITE, 22, Color.TRANSPARENT));
        LinearLayout header = row();
        header.addView(heading("设置与能力", 18), new LinearLayout.LayoutParams(0, -2, 1));
        header.addView(iconButton(Icon.CLOSE, "关闭设置与能力", dialog::dismiss), square(48));
        box.addView(header);
        ScrollView scroll = new ScrollView(activity);
        scroll.setVerticalScrollBarEnabled(false);
        LinearLayout body = column();
        scroll.addView(body);
        detailState = detail(body, "本机状态", detailedState());
        detail(body, "共享与权限", "共享屏幕每次都需系统授权，通知中可随时停止。远程触控使用无障碍服务，让已获准的控制者点击、拖动和输入；仅观看无需开启。权限可在系统设置撤销。");
        Button permissions = button(activity, "管理远程触控权限", false, () -> {
            dialog.dismiss();
            actions.accessibilitySettings();
        });
        body.addView(permissions, new LinearLayout.LayoutParams(-1, -2));
        detail(body, "当前支持", "屏幕共享、9 位临时会议、横屏全屏观看、点击与拖动、常用按键、中文文本输入。");
        detail(body, "暂不支持", "系统声音、麦克风、摄像头、文件传输和剪贴板同步。受保护的画面可能无法共享。");
        detail(body, "关于 YuDesk", version() + " · Android 预览版\n设计者 郁从根 · 17739798184");
        box.addView(scroll, new LinearLayout.LayoutParams(-1, -2));
        dialog.setContentView(box);
        styleDialog(activity, dialog);
        dialog.setOnShowListener(ignored -> {
            sizeDialog(activity, dialog);
            // Long capability text scrolls inside the dialog, including on small or landscape screens.
            int max = Math.round(activity.getResources().getDisplayMetrics().heightPixels * .60f);
            scroll.measure(View.MeasureSpec.makeMeasureSpec(Math.min(activity.getResources().getDisplayMetrics().widthPixels - dp(72), dp(380)), View.MeasureSpec.AT_MOST), View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED));
            scroll.getLayoutParams().height = Math.min(scroll.getMeasuredHeight(), max);
            scroll.requestLayout();
        });
        dialog.setOnDismissListener(ignored -> {
            details = null;
            detailState = null;
        });
        dialog.show();
    }

    private TextView detail(LinearLayout parent, String title, String value) {
        TextView heading = heading(title, 12);
        heading.setPadding(0, dp(14), 0, dp(6));
        parent.addView(heading);
        TextView body = label(value, 13, MUTED);
        body.setLineSpacing(dp(3), 1);
        body.setPadding(0, 0, 0, dp(8));
        parent.addView(body);
        return body;
    }

    private String version() {
        try { return "v" + activity.getPackageManager().getPackageInfo(activity.getPackageName(), 0).versionName; }
        catch (Exception ignored) { return "预览版"; }
    }

    static String groupCode(String value) {
        if (!value.matches("[0-9]{6}|[0-9]{9}")) return value;
        StringBuilder grouped = new StringBuilder();
        for (int i = 0; i < value.length(); i++) {
            if (i > 0 && i % 3 == 0) grouped.append(' ');
            grouped.append(value.charAt(i));
        }
        return grouped.toString();
    }

    private EditText input(String hint, int length, boolean password) {
        EditText field = new EditText(activity);
        field.setSingleLine(true);
        field.setTextSize(16);
        field.setTextColor(INK);
        field.setHintTextColor(MUTED);
        field.setHint(hint);
        field.setInputType(InputType.TYPE_CLASS_NUMBER | (password ? InputType.TYPE_NUMBER_VARIATION_PASSWORD : 0));
        if (password) field.setTransformationMethod(PasswordTransformationMethod.getInstance());
        field.setFilters(new InputFilter[]{(source, start, end, dest, dstart, dend) -> {
            String original = source.subSequence(start, end).toString();
            String normalized = original.replaceAll("[\\s\\u00a0\\u2007\\u202f-]", "");
            return original.equals(normalized) ? null : normalized;
        }, new InputFilter.LengthFilter(length)});
        field.setBackground(null);
        field.setBackgroundTintList(null);
        field.setPadding(dp(13), dp(12), dp(13), dp(12));
        field.setMinimumHeight(dp(48));
        field.setSelectAllOnFocus(false);
        field.setImportantForAutofill(View.IMPORTANT_FOR_AUTOFILL_NO);
        return field;
    }

    private RadioButton mode(String title) {
        RadioButton choice = new RadioButton(activity);
        choice.setId(View.generateViewId());
        choice.setButtonDrawable((Drawable) null);
        choice.setBackgroundTintList(null);
        choice.setText(title);
        choice.setTextSize(13);
        choice.setTypeface(MEDIUM);
        choice.setGravity(Gravity.CENTER);
        choice.setMinWidth(0);
        choice.setMinimumWidth(0);
        choice.setMinHeight(dp(48));
        choice.setMinimumHeight(dp(48));
        choice.setPadding(dp(6), dp(8), dp(6), dp(8));
        choice.setTextColor(new ColorStateList(new int[][]{new int[]{android.R.attr.state_checked}, new int[]{}}, new int[]{BLUE, MUTED}));
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{android.R.attr.state_checked}, shape(activity, Color.WHITE, 9, 0xffd4e2f7));
        background.addState(new int[]{}, shape(activity, Color.TRANSPARENT, 9, Color.TRANSPARENT));
        choice.setBackground(new RippleDrawable(ColorStateList.valueOf(0x181769e8), background, shape(activity, Color.WHITE, 9, Color.TRANSPARENT)));
        choice.setStateListAnimator(null);
        choice.setElevation(0);
        return choice;
    }

    static Button button(Context context, String title, boolean primary, Runnable action) {
        Button button = new Button(context);
        button.setText(title);
        button.setTextSize(13);
        button.setAllCaps(false);
        button.setTypeface(MEDIUM);
        button.setMinWidth(0);
        button.setMinimumWidth(0);
        button.setMinHeight(dp(context, 48));
        button.setMinimumHeight(dp(context, 48));
        button.setPadding(dp(context, 12), dp(context, 9), dp(context, 12), dp(context, 9));
        button.setIncludeFontPadding(false);
        button.setGravity(Gravity.CENTER);
        button.setBackgroundTintList(null);
        button.setTextColor(new ColorStateList(new int[][]{new int[]{-android.R.attr.state_enabled}, new int[]{}}, new int[]{primary ? 0xffedf3ff : MUTED, primary ? Color.WHITE : BLUE}));
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{-android.R.attr.state_enabled}, shape(context, primary ? 0xff99b9eb : FIELD, 11, Color.TRANSPARENT));
        background.addState(new int[]{android.R.attr.state_focused}, shape(context, primary ? BLUE : PALE_BLUE, 11, primary ? 0xff0c3e98 : BLUE));
        background.addState(new int[]{}, shape(context, primary ? BLUE : PALE_BLUE, 11, Color.TRANSPARENT));
        button.setBackground(new RippleDrawable(ColorStateList.valueOf(primary ? 0x33ffffff : 0x181769e8), background, shape(context, Color.WHITE, 11, Color.TRANSPARENT)));
        button.setStateListAnimator(null);
        button.setElevation(0);
        button.setCompoundDrawablePadding(dp(context, 10));
        button.setOnClickListener(view -> action.run());
        return button;
    }

    static void styleDialog(Activity activity, Dialog dialog) {
        sizeDialog(activity, dialog);
        dialog.setOnShowListener(ignored -> sizeDialog(activity, dialog));
    }

    private static void sizeDialog(Activity activity, Dialog dialog) {
        Window window = dialog.getWindow();
        if (window == null) return;
        window.setBackgroundDrawableResource(android.R.color.transparent);
        window.setLayout(Math.min(activity.getResources().getDisplayMetrics().widthPixels - dp(activity, 32), dp(activity, 420)), -2);
        window.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
        WindowManager.LayoutParams params = window.getAttributes();
        params.dimAmount = .24f;
        window.setAttributes(params);
        window.getDecorView().setElevation(0);
    }

    private void quiet(Button button) {
        button.setTextColor(MUTED);
        button.setBackground(ripple(activity, Color.TRANSPARENT, 10, Color.TRANSPARENT));
    }

    private LinearLayout rowLabels(LinearLayout row, String title) {
        LinearLayout labels = column();
        labels.setPadding(dp(9), dp(4), dp(8), dp(4));
        TextView name = label(title, 13, INK);
        name.setTypeface(MEDIUM);
        labels.addView(name);
        row.addView(labels, new LinearLayout.LayoutParams(0, -2, 1));
        return labels;
    }

    private LinearLayout card(LinearLayout parent, int fill, int border) {
        LinearLayout card = column();
        card.setPadding(dp(16), dp(14), dp(16), dp(14));
        card.setBackground(shape(activity, fill, 18, border));
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2);
        params.bottomMargin = dp(12);
        parent.addView(card, params);
        return card;
    }

    private TextView label(String value, int size, int color) {
        TextView text = new TextView(activity);
        text.setText(value);
        text.setTextSize(size);
        text.setTextColor(color);
        text.setIncludeFontPadding(false);
        text.setGravity(Gravity.CENTER_VERTICAL);
        return text;
    }

    private TextView heading(String value, int size) {
        TextView text = label(value, size, INK);
        text.setTypeface(MEDIUM);
        if (Build.VERSION.SDK_INT >= 28) text.setAccessibilityHeading(true);
        return text;
    }

    private ImageButton iconButton(int icon, String description, Runnable action) {
        ImageButton button = new ImageButton(activity);
        button.setImageDrawable(new Icon(activity, icon, BLUE));
        button.setScaleType(ImageView.ScaleType.CENTER);
        button.setPadding(0, 0, 0, 0);
        button.setMinimumWidth(dp(48));
        button.setMinimumHeight(dp(48));
        button.setBackgroundTintList(null);
        button.setBackground(ripple(activity, Color.TRANSPARENT, 12, Color.TRANSPARENT));
        button.setStateListAnimator(null);
        button.setElevation(0);
        describe(button, description);
        button.setOnClickListener(view -> action.run());
        return button;
    }

    private ImageView decorativeIcon(int icon, int color) {
        ImageView image = new ImageView(activity);
        image.setImageDrawable(new Icon(activity, icon, color));
        image.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        return image;
    }

    private static void describe(View view, String description) {
        if (!TextUtils.equals(view.getContentDescription(), description)) {
            view.setContentDescription(description);
            view.setTooltipText(description);
        }
    }

    private static void enable(View view, boolean enabled) {
        if (view.isEnabled() != enabled) {
            view.setEnabled(enabled);
            view.setAlpha(enabled ? 1f : .35f);
        }
    }

    private static void setText(TextView view, String value) {
        if (!value.contentEquals(view.getText())) view.setText(value);
    }

    private LinearLayout column() { LinearLayout layout = new LinearLayout(activity); layout.setOrientation(LinearLayout.VERTICAL); return layout; }
    private LinearLayout row() { LinearLayout layout = new LinearLayout(activity); layout.setGravity(Gravity.CENTER_VERTICAL); return layout; }
    private void space(LinearLayout parent, int height) { parent.addView(new View(activity), new LinearLayout.LayoutParams(1, dp(height))); }
    private void divider(LinearLayout parent, int color) { View divider = new View(activity); divider.setBackgroundColor(color); parent.addView(divider, new LinearLayout.LayoutParams(-1, dp(1))); }
    private LinearLayout.LayoutParams square(int size) { return new LinearLayout.LayoutParams(dp(size), dp(size)); }
    private int dp(int value) { return dp(activity, value); }
    private static int dp(Context context, int value) { return Math.round(value * context.getResources().getDisplayMetrics().density); }

    private static GradientDrawable shape(Context context, int color, int radius, int border) {
        GradientDrawable shape = new GradientDrawable();
        shape.setColor(color);
        shape.setCornerRadius(dp(context, radius));
        if (border != Color.TRANSPARENT) shape.setStroke(dp(context, 1), border);
        return shape;
    }

    private static Drawable fieldBackground(Context context) {
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{android.R.attr.state_focused}, shape(context, 0xfff7faff, 11, BLUE));
        background.addState(new int[]{}, shape(context, FIELD, 11, Color.TRANSPARENT));
        return background;
    }

    private static Drawable ripple(Context context, int color, int radius, int border) {
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{android.R.attr.state_focused}, shape(context, PALE_BLUE, radius, BLUE));
        background.addState(new int[]{}, shape(context, color, radius, border));
        return new RippleDrawable(ColorStateList.valueOf(0x1f1769e8), background, shape(context, Color.WHITE, radius, Color.TRANSPARENT));
    }

    /** Small stroke icons share a 24-unit grid; no emoji, fonts, bitmaps or platform icon differences. */
    private static final class Icon extends Drawable {
        static final int COPY = 0, EYE = 1, EYE_OFF = 2, ROTATE = 3, ARROW = 4;
        static final int MONITOR = 5, TOUCH = 6, SETTINGS = 7, POWER = 8, CLOSE = 9, CHEVRON = 10, DOT = 11;
        private final int type, color, size;
        private final Paint paint = new Paint(Paint.ANTI_ALIAS_FLAG);
        private final Path path = new Path();
        private final RectF rect = new RectF();
        private int alpha = 255;

        Icon(Context context, int type, int color) { this.type = type; this.color = color; size = dp(context, type == DOT ? 12 : 20); }
        @Override public int getIntrinsicWidth() { return size; }
        @Override public int getIntrinsicHeight() { return size; }
        @Override public void draw(Canvas canvas) {
            int save = canvas.save();
            canvas.translate(getBounds().left, getBounds().top);
            canvas.scale(getBounds().width() / 24f, getBounds().height() / 24f);
            paint.setColor(color);
            paint.setAlpha(alpha);
            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(1.8f);
            paint.setStrokeCap(Paint.Cap.ROUND);
            paint.setStrokeJoin(Paint.Join.ROUND);
            path.reset();
            switch (type) {
                case COPY:
                    rect.set(8, 8, 20, 21); canvas.drawRoundRect(rect, 2, 2, paint);
                    path.moveTo(15, 5); path.lineTo(15, 3); path.lineTo(4, 3); path.lineTo(4, 16); path.lineTo(5, 16); canvas.drawPath(path, paint); break;
                case EYE: case EYE_OFF:
                    path.moveTo(2, 12); path.cubicTo(7, 3, 17, 3, 22, 12); path.cubicTo(17, 21, 7, 21, 2, 12); canvas.drawPath(path, paint);
                    canvas.drawCircle(12, 12, 3, paint);
                    if (type == EYE_OFF) canvas.drawLine(3, 3, 21, 21, paint); break;
                case ROTATE:
                    rect.set(4, 4, 20, 20); canvas.drawArc(rect, 25, 295, false, paint);
                    path.moveTo(20, 3); path.lineTo(20, 9); path.lineTo(14, 9); canvas.drawPath(path, paint); break;
                case ARROW:
                    canvas.drawLine(4, 12, 20, 12, paint);
                    path.moveTo(14, 6); path.lineTo(20, 12); path.lineTo(14, 18); canvas.drawPath(path, paint); break;
                case MONITOR:
                    rect.set(3, 4, 21, 17); canvas.drawRoundRect(rect, 2, 2, paint);
                    canvas.drawLine(12, 17, 12, 21, paint); canvas.drawLine(8, 21, 16, 21, paint); break;
                case TOUCH:
                    path.moveTo(5, 3); path.lineTo(6, 20); path.lineTo(10, 15); path.lineTo(14, 22); path.lineTo(17, 20); path.lineTo(13, 13); path.lineTo(20, 12); path.close(); canvas.drawPath(path, paint); break;
                case SETTINGS:
                    canvas.drawLine(4, 6, 20, 6, paint); canvas.drawLine(4, 12, 20, 12, paint); canvas.drawLine(4, 18, 20, 18, paint);
                    canvas.drawCircle(9, 6, 2, paint); canvas.drawCircle(16, 12, 2, paint); canvas.drawCircle(8, 18, 2, paint); break;
                case POWER:
                    rect.set(4, 4, 20, 21); canvas.drawArc(rect, -45, 270, false, paint); canvas.drawLine(12, 2, 12, 11, paint); break;
                case CLOSE:
                    canvas.drawLine(6, 6, 18, 18, paint); canvas.drawLine(18, 6, 6, 18, paint); break;
                case CHEVRON:
                    path.moveTo(9, 5); path.lineTo(16, 12); path.lineTo(9, 19); canvas.drawPath(path, paint); break;
                case DOT:
                    paint.setStyle(Paint.Style.FILL); canvas.drawCircle(12, 12, 5, paint); break;
                default: break;
            }
            canvas.restoreToCount(save);
        }
        @Override public void setAlpha(int alpha) { this.alpha = alpha; invalidateSelf(); }
        @Override public void setColorFilter(ColorFilter filter) { paint.setColorFilter(filter); invalidateSelf(); }
        @Override public int getOpacity() { return PixelFormat.TRANSLUCENT; }
    }
}
