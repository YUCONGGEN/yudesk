package cn.yucg.yudesk;

/** Full-frame geometry shared by rendering and input; no bars or cropped edges. */
final class ScreenMapping {
    final float left, top, width, height;
    final int sourceWidth, sourceHeight;
    ScreenMapping(int viewWidth, int viewHeight, int imageWidth, int imageHeight) {
        sourceWidth = imageWidth; sourceHeight = imageHeight;
        width = Math.max(1, viewWidth); height = Math.max(1, viewHeight);
        left = 0; top = 0;
    }
    boolean contains(float x, float y) { return width > 0 && height > 0 && x >= left && y >= top && x < left + width && y < top + height; }
    int x(float x) { return Math.max(0, Math.min(sourceWidth - 1, (int) ((x - left) * sourceWidth / Math.max(1, width)))); }
    int y(float y) { return Math.max(0, Math.min(sourceHeight - 1, (int) ((y - top) * sourceHeight / Math.max(1, height)))); }
    float viewX(int x) { return left + x * width / Math.max(1, sourceWidth); }
    float viewY(int y) { return top + y * height / Math.max(1, sourceHeight); }
}
