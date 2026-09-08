package cn.yucg.yudesk;

/** Pure geometry shared by rendering and input. Letterbox margins are inert. */
final class ScreenMapping {
    final float left, top, width, height;
    final int sourceWidth, sourceHeight;
    ScreenMapping(int viewWidth, int viewHeight, int imageWidth, int imageHeight) {
        sourceWidth = imageWidth; sourceHeight = imageHeight;
        float scale = Math.min((float) viewWidth / Math.max(1, imageWidth), (float) viewHeight / Math.max(1, imageHeight));
        width = imageWidth * scale; height = imageHeight * scale;
        left = (viewWidth - width) / 2; top = (viewHeight - height) / 2;
    }
    boolean contains(float x, float y) { return width > 0 && height > 0 && x >= left && y >= top && x < left + width && y < top + height; }
    int x(float x) { return Math.max(0, Math.min(sourceWidth - 1, (int) ((x - left) * sourceWidth / Math.max(1, width)))); }
    int y(float y) { return Math.max(0, Math.min(sourceHeight - 1, (int) ((y - top) * sourceHeight / Math.max(1, height)))); }
    float viewX(int x) { return left + x * width / Math.max(1, sourceWidth); }
    float viewY(int y) { return top + y * height / Math.max(1, sourceHeight); }
}
