// The menu bar item and its menu.
//
// The Objective-C side only knows how to build the item and how to stop the
// event loop; the behaviour of the entries lives in Go, which is reached
// through the functions exported by the cgo file.
//
// ARC is on (see the cgo flags in menubar_darwin.go). Without it the status
// item comes back autoreleased, the file scope variable does not keep it
// alive and the menu bar entry disappears the moment the autorelease pool
// drains. The check below turns that mistake into a build error.

#import <Cocoa/Cocoa.h>

#if !__has_feature(objc_arc)
#error "the menu bar needs ARC: the status item would be released right away"
#endif

#include "_cgo_export.h"
#include "menubar_darwin.h"

@interface MymacMenuTarget : NSObject
- (void)openInterface:(id)sender;
- (void)quitApp:(id)sender;
@end

@implementation MymacMenuTarget

- (void)openInterface:(id)sender {
    mymacMenuOpen();
}

- (void)quitApp:(id)sender {
    // The Go side cancels the server first, then the loop is stopped so the
    // process can exit on its own terms.
    mymacMenuQuit();
    [NSApp stop:nil];
}

@end

static MymacMenuTarget *gTarget = nil;
static NSStatusItem *gItem = nil;

// mymac_status_image returns the icon of the status item.
//
// SF Symbols are used when the system offers them; the item falls back to a
// letter on older systems.
static NSImage *mymac_status_image(void) {
    if (@available(macOS 11.0, *)) {
        NSArray<NSString *> *symbols = @[
            @"wrench.and.screwdriver.fill",
            @"gearshape.2.fill",
            @"macwindow",
            @"square.grid.2x2.fill"
        ];
        for (NSString *name in symbols) {
            NSImage *image = [NSImage imageWithSystemSymbolName:name
                                          accessibilityDescription:@"mymac"];
            if (image != nil) {
                image.template = YES;
                return image;
            }
        }
    }
    return nil;
}

// mymac_menubar_create builds the status item and its menu.
//
// It returns 1 on success and 0 when the status bar is not available, which
// happens when the process runs without a window server connection.
int mymac_menubar_create(const char *title, const char *address) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        // An accessory application has no Dock icon; the menu bar item is
        // the only thing on screen.
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        gTarget = [[MymacMenuTarget alloc] init];

        NSMenu *menu = [[NSMenu alloc] init];

        NSMenuItem *open = [[NSMenuItem alloc]
            initWithTitle:[NSString stringWithUTF8String:title]
                   action:@selector(openInterface:)
            keyEquivalent:@"o"];
        [open setTarget:gTarget];
        [menu addItem:open];

        // The address is shown so the user can copy it into another browser.
        NSMenuItem *addr = [[NSMenuItem alloc]
            initWithTitle:[NSString stringWithUTF8String:address]
                   action:NULL
            keyEquivalent:@""];
        [addr setEnabled:NO];
        [menu addItem:addr];

        [menu addItem:[NSMenuItem separatorItem]];

        NSMenuItem *quit = [[NSMenuItem alloc]
            initWithTitle:@"退出 mymac"
                   action:@selector(quitApp:)
            keyEquivalent:@"q"];
        [quit setTarget:gTarget];
        [menu addItem:quit];

        gItem = [[NSStatusBar systemStatusBar]
            statusItemWithLength:NSVariableStatusItemLength];
        if (gItem == nil) {
            return 0;
        }
        [gItem setMenu:menu];

        NSImage *image = mymac_status_image();
        if (image != nil) {
            [[gItem button] setImage:image];
            [[gItem button] setImagePosition:NSImageOnly];
        } else {
            [[gItem button] setTitle:@"M"];
        }
        [[gItem button] setToolTip:@"mymac"];

        // Without a window the item is not on screen, which happens when the
        // process runs without a window server connection. The caller then
        // falls back to running without the menu bar.
        if ([[gItem button] window] == nil) {
            return 0;
        }
        return 1;
    }
}

// mymac_menubar_run enters the Cocoa event loop.
void mymac_menubar_run(void) {
    @autoreleasepool {
        [NSApp run];
    }
}
