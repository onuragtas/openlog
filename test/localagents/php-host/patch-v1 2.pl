#!/usr/bin/perl
# Converts the datagram format of the option-A spike extension (agents/php/prototype-a/ext) to contract v1
# (docs/contracts/php-agent.md §2) at image build time, so the php-host can exercise the infra agent's php_forwarder
# before agents/php/ext exists. Every replacement must apply exactly once, otherwise the build fails.
# SPDX-License-Identifier: Apache-2.0
use strict;
use warnings;

my $file = shift or die "usage: $0 openlog.c\n";
open my $in, '<', $file or die "$file: $!\n";
my $s = do { local $/; <$in> };
close $in;

# rep matches the old lines (surrounding whitespace ignored) and replaces them with the new text.
sub rep {
    my ($what, $old, $new) = @_;
    my $re = join '\s*', map { quotemeta } grep { length } map { s/^\s+|\s+$//gr } split /\n/, $old;
    my $n = () = $s =~ /$re/g;
    die "patch '$what': expected 1 match, found $n\n" if $n != 1;
    $s =~ s/$re/$new/;
}

rep('span id', <<'OLD', <<'NEW');
W_LIT(w, "{\"span_id\":");
OLD
W_LIT(w, "{\"id\":");
NEW

rep('parent id', <<'OLD', <<'NEW');
W_LIT(w, ",\"parent_span_id\":");
OLD
W_LIT(w, ",\"parent\":");
NEW

rep('exception event', <<'OLD', <<'NEW');
W_LIT(w, ",\"exception\":{\"type\":");
w_str(w, s->ex_type);
W_LIT(w, ",\"message\":");
w_str(w, s->ex_msg ? s->ex_msg : "");
W_LIT(w, ",\"where\":");
w_str(w, s->ex_where ? s->ex_where : "");
W_LIT(w, "}");
OLD
W_LIT(w, ",\"events\":[{\"name\":\"exception\",\"time\":");
		w_u64(w, s->start_unix_ns + s->duration_ns);
		W_LIT(w, ",\"attrs\":{\"exception.type\":");
		w_str(w, s->ex_type);
		W_LIT(w, ",\"exception.message\":");
		w_str(w, s->ex_msg ? s->ex_msg : "");
		W_LIT(w, ",\"exception.stacktrace\":");
		w_str(w, s->ex_where ? s->ex_where : "");
		W_LIT(w, "}}]");
NEW

rep('message header', <<'OLD', <<'NEW');
W_LIT(&w, "{\"v\":1,\"service\":");
w_str(&w, svc && *svc ? svc : OLG(service_name));
W_LIT(&w, ",\"trace_id\":");
w_hex(&w, OLG(trace_id), 16);
W_LIT(&w, ",\"spans\":[");
OLD
W_LIT(&w, "{\"v\":1,\"pid\":");
	w_u64(&w, (uint64_t) getpid());
	W_LIT(&w, ",\"trace_id\":");
	w_hex(&w, OLG(trace_id), 16);
	W_LIT(&w, ",\"seq\":0,\"last\":true,\"resource\":{\"service.name\":");
	w_str(&w, svc && *svc ? svc : OLG(service_name));
	W_LIT(&w, ",\"deployment.environment.name\":\"local\",\"process.runtime.name\":\"php\",\"process.runtime.version\":");
	w_str(&w, PHP_VERSION);
	W_LIT(&w, ",\"php.sapi\":");
	w_str(&w, sapi_module.name);
	W_LIT(&w, ",\"telemetry.distro.name\":\"openlog-php\",\"telemetry.distro.version\":\"0.0.1-prototype-a\"}");
	W_LIT(&w, ",\"sampling_ratio\":1,\"function_trace\":false,\"spans\":[");
NEW

rep('dropped spans', <<'OLD', <<'NEW');
W_LIT(&w, "],\"dropped\":");
OLD
W_LIT(&w, "],\"dropped_spans\":");
NEW

open my $out, '>', $file or die "$file: $!\n";
print $out $s;
close $out;
print "patched $file to php-agent contract v1\n";
