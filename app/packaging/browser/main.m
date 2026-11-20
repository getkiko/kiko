// kiko-browser is Kiko's built-in browser: a WKWebView window whose every
// connection goes through one outbound, by way of the HTTP proxy the core
// serves for it on a loopback port.
//
//   kiko-browser <proxy http://127.0.0.1:port> <url> <profile uuid> <outbound> <webrtc|no-webrtc>
//
// The profile names the window's own website data store, the outbound's
// cookies and logins kept from one run to the next. It writes READY once
// the window shows, or "ERROR <message>" and exits; each JSON line on its
// input brings the window forward, and {"url": "..."} loads that page.
// The end of its input closes it.
// It needs macOS 14 for a data store's own proxy.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <Network/Network.h>
#import <objc/message.h>

@interface KikoBrowser : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate>
@property NSWindow *window;
@property WKWebView *web;
@property NSTextField *address;
@property NSTextField *status;
@property NSString *outbound;
@property NSString *target;
@property NSMutableData *input;
@end

static void report(NSString *line) {
    puts(line.UTF8String);
    fflush(stdout);
}

static BOOL webURL(NSURL *url) {
    NSString *scheme = url.scheme.lowercaseString;
    return url.host.length > 0 && ([scheme isEqual:@"https"] || [scheme isEqual:@"http"]);
}

@implementation KikoBrowser

- (NSButton *)toolbarButton:(NSString *)symbol label:(NSString *)label action:(SEL)action {
    NSImage *image = [NSImage imageWithSystemSymbolName:symbol accessibilityDescription:label];
    NSButton *button = [NSButton buttonWithImage:image target:self action:action];
    button.bezelStyle = NSBezelStyleTexturedRounded;
    button.bordered = NO;
    button.toolTip = label;
    return button;
}

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    if (@available(macOS 14.0, *)) {
        NSArray<NSString *> *args = NSProcessInfo.processInfo.arguments;
        if (args.count != 6 || !webURL([NSURL URLWithString:args[2]])) {
            report(@"ERROR 浏览器启动参数无效");
            [NSApp terminate:nil];
            return;
        }
        NSURL *proxy = [NSURL URLWithString:args[1]];
        if (![proxy.host isEqual:@"127.0.0.1"] || proxy.port.intValue <= 0) {
            report(@"ERROR 本机代理地址无效");
            [NSApp terminate:nil];
            return;
        }
        NSUUID *profile = [[NSUUID alloc] initWithUUIDString:args[3]];
        if (!profile) {
            report(@"ERROR 浏览器资料标识无效");
            [NSApp terminate:nil];
            return;
        }
        self.target = args[2];
        self.outbound = args[4];

        WKWebViewConfiguration *config = [WKWebViewConfiguration new];
        config.websiteDataStore = [WKWebsiteDataStore dataStoreForIdentifier:profile];
        // Every connection goes to the core's proxy, and none goes around
        // it when the proxy fails.
        nw_endpoint_t endpoint = nw_endpoint_create_host("127.0.0.1", proxy.port.stringValue.UTF8String);
        nw_proxy_config_t proxyConfig = nw_proxy_config_create_http_connect(endpoint, NULL);
        nw_proxy_config_set_failover_allowed(proxyConfig, false);
        nw_proxy_config_clear_excluded_domains(proxyConfig);
        config.websiteDataStore.proxyConfigurations = @[ proxyConfig ];
        config.mediaTypesRequiringUserActionForPlayback = WKAudiovisualMediaTypeAll;
        // WebRTC sends its UDP around the proxy, telling the real address,
        // so it is off unless asked for. WebKit turns it off for every
        // frame; the script, for a WebKit without the preference, hides it
        // in the frames it runs in.
        if (![args[5] isEqual:@"webrtc"]) {
            SEL peerConnection = NSSelectorFromString(@"_setPeerConnectionEnabled:");
            if ([config.preferences respondsToSelector:peerConnection]) {
                ((void (*)(id, SEL, BOOL))objc_msgSend)(config.preferences, peerConnection, NO);
            }
            NSString *noRTC = @"for(const k of ['RTCPeerConnection','webkitRTCPeerConnection']){try{Object.defineProperty(globalThis,k,{value:undefined,writable:false,configurable:false})}catch{}}";
            [config.userContentController addUserScript:[[WKUserScript alloc] initWithSource:noRTC injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:NO]];
        }
        [self showWindow:config];
        report(@"READY");
        [self navigate:self.target];
        [self readCommands];
    } else {
        report(@"ERROR 内置浏览器需要 macOS 14 或更新版本，请使用 Chrome 打开");
        [NSApp terminate:nil];
    }
}

- (void)showWindow:(WKWebViewConfiguration *)config {
    NSMenu *menu = [NSMenu new];
    NSMenuItem *appItem = [NSMenuItem new];
    [menu addItem:appItem];
    NSMenu *app = [NSMenu new];
    appItem.submenu = app;
    [app addItemWithTitle:@"关闭浏览器" action:@selector(terminate:) keyEquivalent:@"q"];
    NSMenuItem *editItem = [NSMenuItem new];
    [menu addItem:editItem];
    NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
    editItem.submenu = edit;
    [edit addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
    [edit addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [edit addItemWithTitle:@"复制" action:@selector(copy:) keyEquivalent:@"c"];
    [edit addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [edit addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    NSMenuItem *browserItem = [NSMenuItem new];
    [menu addItem:browserItem];
    NSMenu *browser = [[NSMenu alloc] initWithTitle:@"浏览"];
    browserItem.submenu = browser;
    for (NSArray *entry in @[ @[ @"地址栏", @"focusAddress:", @"l" ], @[ @"刷新", @"reload:", @"r" ], @[ @"后退", @"back:", @"[" ], @[ @"前进", @"forward:", @"]" ], @[ @"关闭窗口", @"closeWindow:", @"w" ] ]) {
        NSMenuItem *item = [browser addItemWithTitle:entry[0] action:NSSelectorFromString(entry[1]) keyEquivalent:entry[2]];
        item.target = self;
    }
    NSApp.mainMenu = menu;

    self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 1120, 800)
                                              styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    self.window.title = [NSString stringWithFormat:@"%@ · Kiko 浏览器", self.outbound];
    self.window.releasedWhenClosed = NO;
    self.window.delegate = self;
    self.window.minSize = NSMakeSize(640, 420);
    self.window.tabbingMode = NSWindowTabbingModeDisallowed;
    NSView *content = self.window.contentView;

    self.address = [NSTextField new];
    self.address.placeholderString = @"输入网址";
    self.address.bezelStyle = NSTextFieldRoundedBezel;
    self.address.target = self;
    self.address.action = @selector(go:);
    NSStackView *toolbar = [NSStackView stackViewWithViews:@[
        [self toolbarButton:@"chevron.left" label:@"后退" action:@selector(back:)],
        [self toolbarButton:@"chevron.right" label:@"前进" action:@selector(forward:)],
        [self toolbarButton:@"arrow.clockwise" label:@"刷新" action:@selector(reload:)],
        self.address
    ]];
    toolbar.spacing = 6;
    toolbar.edgeInsets = NSEdgeInsetsMake(8, 10, 8, 12);
    toolbar.translatesAutoresizingMaskIntoConstraints = NO;
    [content addSubview:toolbar];

    self.status = [NSTextField labelWithString:[self routeText]];
    self.status.font = [NSFont systemFontOfSize:11];
    self.status.textColor = NSColor.secondaryLabelColor;
    self.status.lineBreakMode = NSLineBreakByTruncatingMiddle;
    self.status.translatesAutoresizingMaskIntoConstraints = NO;
    [content addSubview:self.status];

    self.web = [[WKWebView alloc] initWithFrame:NSZeroRect configuration:config];
    self.web.navigationDelegate = self;
    self.web.UIDelegate = self;
    self.web.allowsBackForwardNavigationGestures = YES;
    // Right click, Inspect Element: the page's requests and console.
    if (@available(macOS 13.3, *)) self.web.inspectable = YES;
    self.web.translatesAutoresizingMaskIntoConstraints = NO;
    [content addSubview:self.web];
    [NSLayoutConstraint activateConstraints:@[
        [toolbar.topAnchor constraintEqualToAnchor:content.topAnchor],
        [toolbar.leadingAnchor constraintEqualToAnchor:content.leadingAnchor],
        [toolbar.trailingAnchor constraintEqualToAnchor:content.trailingAnchor],
        [toolbar.heightAnchor constraintEqualToConstant:44],
        [self.address.widthAnchor constraintGreaterThanOrEqualToConstant:280],
        [self.web.topAnchor constraintEqualToAnchor:toolbar.bottomAnchor],
        [self.web.leadingAnchor constraintEqualToAnchor:content.leadingAnchor],
        [self.web.trailingAnchor constraintEqualToAnchor:content.trailingAnchor],
        [self.web.bottomAnchor constraintEqualToAnchor:self.status.topAnchor constant:-5],
        [self.status.leadingAnchor constraintEqualToAnchor:content.leadingAnchor constant:12],
        [self.status.trailingAnchor constraintEqualToAnchor:content.trailingAnchor constant:-12],
        [self.status.bottomAnchor constraintEqualToAnchor:content.bottomAnchor constant:-5],
        [self.status.heightAnchor constraintEqualToConstant:16]
    ]];
    [self.window center];
    [self.window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
}

- (NSString *)routeText {
    return [NSString stringWithFormat:@"经由出站「%@」", self.outbound];
}

- (void)navigate:(NSString *)text {
    NSURL *url = [NSURL URLWithString:text];
    if (!webURL(url)) {
        self.status.stringValue = @"请输入 HTTP 或 HTTPS 网页地址";
        return;
    }
    self.address.stringValue = url.absoluteString;
    [self.web loadRequest:[NSURLRequest requestWithURL:url]];
}

- (void)go:(id)sender {
    NSString *value = [self.address.stringValue stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceCharacterSet];
    if (value.length == 0) return;
    if (![value containsString:@"://"]) value = [@"https://" stringByAppendingString:value];
    [self navigate:value];
}

- (void)back:(id)sender { [self.web goBack]; }
- (void)forward:(id)sender { [self.web goForward]; }
- (void)reload:(id)sender { [self.web reload]; }
- (void)focusAddress:(id)sender {
    [self.window makeFirstResponder:self.address];
    [self.address selectText:nil];
}
- (void)closeWindow:(id)sender { [self.window performClose:nil]; }

- (void)readCommands {
    self.input = [NSMutableData new];
    NSFileHandle.fileHandleWithStandardInput.readabilityHandler = ^(NSFileHandle *handle) {
        NSData *data = handle.availableData;
        dispatch_async(dispatch_get_main_queue(), ^{
            if (data.length == 0) {
                [NSApp terminate:nil];
                return;
            }
            [self.input appendData:data];
            while (YES) {
                NSRange newline = [self.input rangeOfData:[NSData dataWithBytes:"\n" length:1] options:0 range:NSMakeRange(0, self.input.length)];
                if (newline.location == NSNotFound) break;
                NSData *line = [self.input subdataWithRange:NSMakeRange(0, newline.location)];
                [self.input replaceBytesInRange:NSMakeRange(0, newline.location + 1) withBytes:NULL length:0];
                NSDictionary *command = [NSJSONSerialization JSONObjectWithData:line options:0 error:NULL];
                if ([command isKindOfClass:NSDictionary.class] && [command[@"url"] isKindOfClass:NSString.class]) {
                    [self navigate:command[@"url"]];
                }
                [self.window makeKeyAndOrderFront:nil];
                [NSApp activateIgnoringOtherApps:YES];
            }
        });
    };
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
    NSString *scheme = action.request.URL.scheme.lowercaseString;
    BOOL page = [scheme isEqual:@"http"] || [scheme isEqual:@"https"] || [scheme isEqual:@"about"] || [scheme isEqual:@"data"] || [scheme isEqual:@"blob"];
    handler(page && !action.shouldPerformDownload ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

// A page that opens a window shows it in this one.
- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)config forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
    if (webURL(action.request.URL)) [self navigate:action.request.URL.absoluteString];
    return nil;
}

- (void)webView:(WKWebView *)webView requestMediaCapturePermissionForOrigin:(WKSecurityOrigin *)origin initiatedByFrame:(WKFrameInfo *)frame type:(WKMediaCaptureType)type decisionHandler:(void (^)(WKPermissionDecision))handler {
    handler(WKPermissionDecisionDeny);
}

- (void)webView:(WKWebView *)webView didStartProvisionalNavigation:(WKNavigation *)navigation {
    self.status.stringValue = [NSString stringWithFormat:@"%@ · 正在加载…", [self routeText]];
}

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
    self.address.stringValue = webView.URL.absoluteString ?: @"";
    self.status.stringValue = [self routeText];
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    if (error.code == NSURLErrorCancelled) return;
    self.status.stringValue = [NSString stringWithFormat:@"%@ · 访问失败：%@", [self routeText], error.localizedDescription];
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self webView:webView didFailProvisionalNavigation:navigation withError:error];
}

- (void)windowWillClose:(NSNotification *)notification { [NSApp terminate:nil]; }

@end

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        NSApplication *app = NSApplication.sharedApplication;
        // No Dock icon of its own: it is a window of Kiko.
        [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
        KikoBrowser *delegate = [KikoBrowser new];
        app.delegate = delegate;
        [app run];
    }
    return 0;
}
