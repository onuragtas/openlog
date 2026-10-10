// What a role may do, as the server decides and the web mirrors.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/roles.dart';

void main() {
  test('the table matches the web, tab by tab', () {
    // The settings tabs the web gates, with the role each one needs.
    expect(can(Role.member, 'license_keys.list'), isTrue);
    expect(can(Role.viewer, 'license_keys.list'), isFalse);
    expect(can(Role.member, 'api_keys.list'), isTrue);
    expect(can(Role.member, 'browser_keys.list'), isTrue);
    expect(can(Role.member, 'source_maps.list'), isTrue);
    expect(can(Role.member, 'org.update'), isFalse);
    expect(can(Role.admin, 'org.update'), isTrue);
    expect(can(Role.member, 'audit.read'), isFalse);
    expect(can(Role.admin, 'audit.read'), isTrue);
    expect(can(Role.admin, 'disk_space.read'), isTrue);
  });

  test('an unknown permission is denied, not allowed', () {
    // A typo should hide a button, not offer one that answers 403.
    expect(can(Role.owner, 'nothing.likeThis'), isFalse);
  });

  test('no role at all can do nothing', () {
    expect(can(null, 'api_keys.list'), isFalse);
    expect(atLeast(null, Role.viewer), isFalse);
  });

  test('only owners hand out owner', () {
    expect(assignableRoles(Role.owner, Role.member), [
      Role.owner,
      Role.admin,
      Role.member,
      Role.viewer,
    ]);
    // An admin may change a member's role, but cannot make one an owner.
    expect(assignableRoles(Role.admin, Role.member), [
      Role.admin,
      Role.member,
      Role.viewer,
    ]);
    // And cannot touch an owner at all.
    expect(assignableRoles(Role.admin, Role.owner), isEmpty);
    expect(assignableRoles(Role.member, Role.member), isEmpty);
  });
}
