// Code-native rendering of internal/viewerapp/ui/icon.svg's blue Yu paths.
// This asset tool runs only on the Mac performing packaging; it is not shipped.
#import <AppKit/AppKit.h>

static BOOL writeIcon(NSString *directory, NSString *name, NSInteger pixels) {
    NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc]
        initWithBitmapDataPlanes:NULL pixelsWide:pixels pixelsHigh:pixels
        bitsPerSample:8 samplesPerPixel:4 hasAlpha:YES isPlanar:NO
        colorSpaceName:NSDeviceRGBColorSpace bytesPerRow:0 bitsPerPixel:0];
    if (!bitmap) return NO;
    NSGraphicsContext *graphics = [NSGraphicsContext graphicsContextWithBitmapImageRep:bitmap];
    [NSGraphicsContext saveGraphicsState];
    [NSGraphicsContext setCurrentContext:graphics];
    CGContextRef context = graphics.CGContext;
    CGContextClearRect(context, CGRectMake(0, 0, pixels, pixels));
    CGContextTranslateCTM(context, 0, pixels);
    CGContextScaleCTM(context, pixels / 64.0, -pixels / 64.0);
    CGPathRef background = CGPathCreateWithRoundedRect(CGRectMake(0, 0, 64, 64), 15, 15, NULL);
    CGContextSaveGState(context);
    CGContextAddPath(context, background);
    CGContextClip(context);
    CGColorSpaceRef colors = CGColorSpaceCreateDeviceRGB();
    const CGFloat stops[] = {39.0/255,141.0/255,1,1, 18.0/255,100.0/255,232.0/255,1};
    CGGradientRef gradient = CGGradientCreateWithColorComponents(colors, stops, NULL, 2);
    CGContextDrawLinearGradient(context, gradient, CGPointMake(32, 0), CGPointMake(32, 64), 0);
    CGContextRestoreGState(context);
    CGGradientRelease(gradient);
    CGColorSpaceRelease(colors);
    CGPathRelease(background);
    CGContextSetRGBStrokeColor(context, 1, 1, 1, 1);
    CGContextSetLineWidth(context, 5.5);
    CGContextSetLineCap(context, kCGLineCapRound);
    CGContextSetLineJoin(context, kCGLineJoinRound);
    CGContextMoveToPoint(context, 12, 18);
    CGContextAddLineToPoint(context, 22, 33);
    CGContextAddLineToPoint(context, 32, 18);
    CGContextMoveToPoint(context, 22, 33);
    CGContextAddLineToPoint(context, 22, 48);
    CGContextMoveToPoint(context, 36, 29);
    CGContextAddLineToPoint(context, 36, 41);
    CGContextAddCurveToPoint(context, 36, 46, 39, 48, 43, 48);
    CGContextAddCurveToPoint(context, 47, 48, 51, 45, 51, 41);
    CGContextAddLineToPoint(context, 51, 29);
    CGContextStrokePath(context);
    [NSGraphicsContext restoreGraphicsState];
    NSData *png = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    return png && [png writeToFile:[directory stringByAppendingPathComponent:name] options:NSDataWritingWithoutOverwriting error:NULL];
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc != 2) { fprintf(stderr, "usage: render-icon EXISTING_ICONSET_DIR\n"); return 2; }
        NSString *directory = [NSString stringWithUTF8String:argv[1]];
        BOOL isDirectory = NO;
        if (![[NSFileManager defaultManager] fileExistsAtPath:directory isDirectory:&isDirectory] || !isDirectory) return 2;
        const NSInteger sizes[] = {16, 32, 128, 256, 512};
        for (NSUInteger i = 0; i < sizeof(sizes)/sizeof(sizes[0]); i++) {
            NSInteger size = sizes[i];
            for (NSInteger scale = 1; scale <= 2; scale++) {
                NSString *name = [NSString stringWithFormat:@"icon_%ldx%ld%@.png", (long)size, (long)size, scale == 2 ? @"@2x" : @""];
                if (!writeIcon(directory, name, size * scale)) {
                    fprintf(stderr, "Cannot create icon: %s\n", name.UTF8String);
                    return 1;
                }
            }
        }
    }
    return 0;
}
