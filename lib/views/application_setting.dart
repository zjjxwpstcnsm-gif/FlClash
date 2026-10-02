import 'dart:async';

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/core/core.dart';
import 'package:fl_clash/providers/config.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

class SmartFailoverItem extends ConsumerWidget {
  const SmartFailoverItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final enabled = ref.watch(
      appSettingProvider.select((state) => state.smartFailover),
    );
    return ListItem.toggle(
      leading: const Icon(Icons.sync_alt),
      title: Text(context.appLocalizations.smartFailover),
      subtitle: Text(context.appLocalizations.smartFailoverDesc),
      value: enabled,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(smartFailover: value));
      },
    );
  }
}

class SmartFailoverMaxDelayItem extends ConsumerWidget {
  const SmartFailoverMaxDelayItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l10n = context.appLocalizations;
    final limit = ref.watch(
      appSettingProvider.select((state) => state.smartFailoverMaxDelayMs),
    );
    return ListItem.input(
      leading: const Icon(Icons.speed),
      title: Text(l10n.smartFailoverMaxDelay),
      subtitle: Text(l10n.smartFailoverMaxDelayDesc(limit)),
      dialogTitle: l10n.smartFailoverMaxDelay,
      value: '$limit',
      suffixText: 'ms',
      resetValue: '200',
      maxLength: 4,
      keyboardType: TextInputType.number,
      validator: (value) {
        final parsed = int.tryParse(value?.trim() ?? '');
        return parsed == null || parsed < 50 || parsed > 3000
            ? l10n.smartFailoverLatencyRange
            : null;
      },
      onChanged: (value) {
        final parsed = int.tryParse(value?.trim() ?? '');
        if (parsed == null || parsed < 50 || parsed > 3000) return;
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(smartFailoverMaxDelayMs: parsed));
      },
    );
  }
}

class SmartFailoverStatusItem extends ConsumerStatefulWidget {
  const SmartFailoverStatusItem({super.key, this.controller});

  final CoreController? controller;

  @override
  ConsumerState<SmartFailoverStatusItem> createState() =>
      _SmartFailoverStatusItemState();
}

class _SmartFailoverStatusItemState
    extends ConsumerState<SmartFailoverStatusItem> {
  Timer? _timer;
  Map<String, dynamic> _status = {};
  bool _busy = false;
  bool _failed = false;
  int _revision = 0;

  @override
  void initState() {
    super.initState();
    ref.listenManual(
      appSettingProvider.select((state) => state.smartFailover),
      (_, enabled) {
        _revision++;
        _timer?.cancel();
        _timer = null;
        _busy = false;
        _status = {};
        if (enabled) {
          unawaited(_poll());
          _timer = Timer.periodic(
            const Duration(seconds: 2),
            (_) => unawaited(_poll()),
          );
        }
      },
      fireImmediately: true,
    );
  }

  Future<void> _poll({bool recheck = false}) async {
    if (_busy) return;
    final revision = _revision;
    setState(() => _busy = true);
    try {
      final status = await (widget.controller ?? coreController)
          .getSmartFailoverStatus(recheck: recheck);
      if (!mounted || revision != _revision) return;
      setState(() {
        _status = status;
        _failed = false;
      });
    } catch (_) {
      if (mounted && revision == _revision) {
        setState(() => _failed = true);
      }
    } finally {
      if (mounted && revision == _revision) {
        setState(() => _busy = false);
      }
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final enabled = ref.watch(
      appSettingProvider.select((state) => state.smartFailover),
    );
    if (!enabled) return const SizedBox.shrink();
    final l10n = context.appLocalizations;
    final count = (_status['candidateCount'] as num?)?.toInt() ?? 0;
    final limit = ref.watch(appSettingProvider).smartFailoverMaxDelayMs;
    final ready = _status['state'] == 'ready';
    final direct = _status['mode']?.toString().toLowerCase() == 'direct';
    final message = _failed
        ? l10n.smartFailoverStatusError
        : direct && _status['running'] == true
        ? l10n.smartFailoverDirect
        : switch (_status['state']) {
            'ready' => l10n.smartFailoverReady(
              _status['current'] as String? ?? '',
              (_status['delayMs'] as num?)?.toInt() ?? 0,
            ),
            'stopped' => l10n.smartFailoverStopped,
            'empty' => l10n.smartFailoverEmpty,
            'unavailable' => l10n.smartFailoverUnavailable(count, limit),
            _ => l10n.smartFailoverChecking(count),
          };
    final results = (_status['results'] as Map?) ?? {};
    final reasons = results.values.whereType<Map>().map(
      (result) => result['reason']?.toString() ?? '',
    );
    final refused = reasons.where(
      (reason) => reason.startsWith('service_refused:'),
    );
    final codes = refused.map((reason) => reason.split(':').last).toSet();
    return ListItem(
      leading: Icon(ready && !direct ? Icons.verified : Icons.network_check),
      title: Text(l10n.smartFailoverStatus),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(message),
          if (results.isNotEmpty)
            Text(
              l10n.smartFailoverDiagnostics(
                results.length,
                reasons.where((reason) => reason == 'latency_limit').length,
                refused.length,
                reasons
                    .where(
                      (reason) =>
                          reason.isNotEmpty &&
                          reason != 'latency_limit' &&
                          reason != 'exit_region' &&
                          !reason.startsWith('service_refused:'),
                    )
                    .length,
                reasons.where((reason) => reason == 'exit_region').length,
              ),
            ),
          if (codes.isNotEmpty)
            Text(l10n.smartFailoverRefusedCodes(codes.join(', '))),
        ],
      ),
      trailing: IconButton(
        tooltip: l10n.smartFailoverCheckNow,
        onPressed: _busy || _status['running'] != true
            ? null
            : () => unawaited(_poll(recheck: true)),
        icon: const Icon(Icons.refresh),
      ),
    );
  }
}

class CloseConnectionsItem extends ConsumerWidget {
  const CloseConnectionsItem({super.key});

  @override
  Widget build(BuildContext context, ref) {
    final appLocalizations = context.appLocalizations;
    final closeConnections = ref.watch(
      appSettingProvider.select((state) => state.closeConnections),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.autoCloseConnections),
      subtitle: Text(appLocalizations.autoCloseConnectionsDesc),
      value: closeConnections,
      onChanged: (value) async {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(closeConnections: value));
      },
    );
  }
}

class UsageItem extends ConsumerWidget {
  const UsageItem({super.key});

  @override
  Widget build(BuildContext context, ref) {
    final appLocalizations = context.appLocalizations;
    final onlyStatisticsProxy = ref.watch(
      appSettingProvider.select((state) => state.onlyStatisticsProxy),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.onlyStatisticsProxy),
      subtitle: Text(appLocalizations.onlyStatisticsProxyDesc),
      value: onlyStatisticsProxy,
      onChanged: (bool value) async {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(onlyStatisticsProxy: value));
      },
    );
  }
}

class MinimizeItem extends ConsumerWidget {
  const MinimizeItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final minimizeOnExit = ref.watch(
      appSettingProvider.select((state) => state.minimizeOnExit),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.minimizeOnExit),
      subtitle: Text(appLocalizations.minimizeOnExitDesc),
      value: minimizeOnExit,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(minimizeOnExit: value));
      },
    );
  }
}

class AutoLaunchItem extends ConsumerWidget {
  const AutoLaunchItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final autoLaunch = ref.watch(
      appSettingProvider.select((state) => state.autoLaunch),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.autoLaunch),
      subtitle: Text(appLocalizations.autoLaunchDesc),
      value: autoLaunch,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(autoLaunch: value));
      },
    );
  }
}

class SilentLaunchItem extends ConsumerWidget {
  const SilentLaunchItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final silentLaunch = ref.watch(
      appSettingProvider.select((state) => state.silentLaunch),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.silentLaunch),
      subtitle: Text(appLocalizations.silentLaunchDesc),
      value: silentLaunch,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(silentLaunch: value));
      },
    );
  }
}

class AutoRunItem extends ConsumerWidget {
  const AutoRunItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final autoRun = ref.watch(
      appSettingProvider.select((state) => state.autoRun),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.autoRun),
      subtitle: Text(appLocalizations.autoRunDesc),
      value: autoRun,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(autoRun: value));
      },
    );
  }
}

class HiddenItem extends ConsumerWidget {
  const HiddenItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final hidden = ref.watch(
      appSettingProvider.select((state) => state.hidden),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.exclude),
      subtitle: Text(appLocalizations.excludeDesc),
      value: hidden,
      onChanged: (value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(hidden: value));
      },
    );
  }
}

class AnimateTabItem extends ConsumerWidget {
  const AnimateTabItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final isAnimateToPage = ref.watch(
      appSettingProvider.select((state) => state.isAnimateToPage),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.tabAnimation),
      subtitle: Text(appLocalizations.tabAnimationDesc),
      value: isAnimateToPage,
      onChanged: (value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(isAnimateToPage: value));
      },
    );
  }
}

class OpenLogsItem extends ConsumerWidget {
  const OpenLogsItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final openLogs = ref.watch(
      appSettingProvider.select((state) => state.openLogs),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.logcat),
      subtitle: Text(appLocalizations.logcatDesc),
      value: openLogs,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(openLogs: value));
      },
    );
  }
}

class CrashlyticsItem extends ConsumerWidget {
  const CrashlyticsItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final crashlytics = ref.watch(
      appSettingProvider.select((state) => state.crashlytics),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.crashlytics),
      subtitle: Text(appLocalizations.crashlyticsTip),
      value: crashlytics,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(crashlytics: value));
      },
    );
  }
}

class AutoCheckUpdateItem extends ConsumerWidget {
  const AutoCheckUpdateItem({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final appLocalizations = context.appLocalizations;
    final autoCheckUpdate = ref.watch(
      appSettingProvider.select((state) => state.autoCheckUpdate),
    );
    return ListItem.toggle(
      title: Text(appLocalizations.autoCheckUpdate),
      subtitle: Text(appLocalizations.autoCheckUpdateDesc),
      value: autoCheckUpdate,
      onChanged: (bool value) {
        ref
            .read(appSettingProvider.notifier)
            .update((state) => state.copyWith(autoCheckUpdate: value));
      },
    );
  }
}

class ApplicationSettingView extends StatelessWidget {
  const ApplicationSettingView({super.key});

  @override
  Widget build(BuildContext context) {
    final List<Widget> items = [
      const SmartFailoverItem(),
      const SmartFailoverMaxDelayItem(),
      const SmartFailoverStatusItem(),
      const MinimizeItem(),
      if (system.isDesktop) ...[
        const AutoLaunchItem(),
        const SilentLaunchItem(),
      ],
      const AutoRunItem(),
      if (system.isAndroid) ...[const HiddenItem()],
      const AnimateTabItem(),
      const OpenLogsItem(),
      const CloseConnectionsItem(),
      const UsageItem(),
      if (system.isAndroid) const CrashlyticsItem(),
      const AutoCheckUpdateItem(),
    ];
    return BaseScaffold(
      title: context.appLocalizations.application,
      body: ListView.separated(
        itemBuilder: (_, index) {
          final item = items[index];
          return item;
        },
        separatorBuilder: (_, _) {
          return const Divider(height: 0);
        },
        itemCount: items.length,
      ),
    );
  }
}
