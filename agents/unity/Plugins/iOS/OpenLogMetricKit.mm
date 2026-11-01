// MetricKit for openlog (docs/contracts/mobile-crash.md §2).
//
// The operating system reports signals, Mach exceptions and Swift traps on a later launch, so nothing here runs inside a
// crashing process. Each report becomes one file, which the C# side reads: no callback crosses into managed code from a
// thread MetricKit chose, and a report survives the game being closed before C# got to it.

#import <Foundation/Foundation.h>
#import <MetricKit/MetricKit.h>

API_AVAILABLE(ios(14.0))
@interface OpenLogMetricKit : NSObject <MXMetricManagerSubscriber>
@property(nonatomic, copy) NSString *directory;
@end

@implementation OpenLogMetricKit

- (void)didReceiveDiagnosticPayloads:(NSArray<MXDiagnosticPayload *> *)payloads API_AVAILABLE(ios(14.0)) {
    for (MXDiagnosticPayload *payload in payloads) {
        for (MXCrashDiagnostic *crash in payload.crashDiagnostics) {
            NSData *tree = [crash.callStackTree JSONRepresentation];
            NSString *stack = tree ? [[NSString alloc] initWithData:tree encoding:NSUTF8StringEncoding] : @"";
            NSDictionary *report = @{
                @"signal" : crash.signal ?: @0,
                @"reason" : crash.terminationReason ?: @"",
                @"stack" : stack ?: @"",
                @"version" : crash.metaData.applicationBuildVersion ?: @"",
                @"at_ms" : @((long long)(payload.timeStampEnd.timeIntervalSince1970 * 1000)),
            };
            NSData *json = [NSJSONSerialization dataWithJSONObject:report options:0 error:nil];
            NSString *name = [[[NSUUID UUID] UUIDString] stringByAppendingPathExtension:@"json"];
            [json writeToFile:[self.directory stringByAppendingPathComponent:name] atomically:YES];
        }
    }
}

@end

extern "C" void OpenLogMetricKitStart(const char *directory) {
    if (@available(iOS 14.0, *)) {
        static OpenLogMetricKit *subscriber;
        if (subscriber != nil) {
            return;
        }
        NSString *dir = [NSString stringWithUTF8String:directory];
        [[NSFileManager defaultManager] createDirectoryAtPath:dir withIntermediateDirectories:YES attributes:nil error:nil];
        subscriber = [[OpenLogMetricKit alloc] init];
        subscriber.directory = dir;
        [[MXMetricManager sharedManager] addSubscriber:subscriber];
    }
}
