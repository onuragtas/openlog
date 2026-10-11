// Which kind of visitor a browser screen is about.
//
// "Is the site fast" and "is it fast on phones" are different questions, and
// the second is the one asked right after the first number disappoints. The
// options are a fixed little set rather than a fetched one: the SDK derives
// both from the user agent (rum.md §2), so a list endpoint would be a
// request to learn four words.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';

const rumDeviceTypes = ['desktop', 'mobile', 'tablet'];
const rumBrowsers = ['Chrome', 'Safari', 'Firefox', 'Edge', 'Opera'];

class RumFacetPicker extends StatelessWidget {
  const RumFacetPicker({
    super.key,
    required this.deviceType,
    required this.browser,
    required this.onChanged,
    this.browsers = true,
  });

  final String deviceType;
  final String browser;
  final void Function(String deviceType, String browser) onChanged;

  /// False where the data cannot answer it: the vitals rollup keeps no
  /// browser column (rum.md §7).
  final bool browsers;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Wrap(
      spacing: 8,
      runSpacing: 8,
      children: [
        _Pick(
          pickKey: const Key('rum-facet-device'),
          value: deviceType,
          label: l.rumFacetAllDevices,
          options: {
            for (final d in rumDeviceTypes)
              d: switch (d) {
                'desktop' => l.rumDeviceDesktop,
                'mobile' => l.rumDeviceMobile,
                _ => l.rumDeviceTablet,
              },
          },
          onChanged: (v) => onChanged(v, browser),
        ),
        if (browsers)
          _Pick(
            pickKey: const Key('rum-facet-browser'),
            value: browser,
            label: l.rumFacetAllBrowsers,
            options: {for (final b in rumBrowsers) b: b},
            onChanged: (v) => onChanged(deviceType, v),
          ),
      ],
    );
  }
}

class _Pick extends StatelessWidget {
  const _Pick({
    required this.pickKey,
    required this.value,
    required this.label,
    required this.options,
    required this.onChanged,
  });

  final Key pickKey;
  final String value;

  /// What "no choice" is called: "All devices" rather than an empty row.
  final String label;
  final Map<String, String> options;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => DropdownButton<String>(
    key: pickKey,
    value: value,
    underline: const SizedBox.shrink(),
    items: [
      DropdownMenuItem(value: '', child: Text(label)),
      for (final e in options.entries)
        DropdownMenuItem(value: e.key, child: Text(e.value)),
    ],
    onChanged: (v) => onChanged(v ?? ''),
  );
}
