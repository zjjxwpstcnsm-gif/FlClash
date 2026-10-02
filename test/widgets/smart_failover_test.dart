import 'dart:convert';

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/common/theme.dart';
import 'package:fl_clash/core/core.dart';
import 'package:fl_clash/core/interface.dart';
import 'package:fl_clash/l10n/l10n.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/app.dart';
import 'package:fl_clash/providers/config.dart';
import 'package:fl_clash/providers/state.dart';
import 'package:fl_clash/state.dart';
import 'package:fl_clash/views/application_setting.dart';
import 'package:flutter/material.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';

class _MockFailoverCore extends Mock implements CoreHandlerInterface {}

void main() {
  testWidgets('status shows the active node and requests an immediate check', (
    tester,
  ) async {
    final container = _container();
    addTearDown(container.dispose);
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: true));
    final handler = _MockFailoverCore();
    when(
      () => handler.getSmartFailoverStatus(recheck: any(named: 'recheck')),
    ).thenAnswer(
      (_) async => {
        'state': 'ready',
        'running': true,
        'mode': 'rule',
        'current': 'Japan 01',
        'delayMs': 80,
        'candidateCount': 2,
      },
    );
    await tester.pumpWidget(
      _TestApp(
        container: container,
        child: SmartFailoverStatusItem(
          controller: CoreController.test(handler),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('Connected: Japan 01 · 80 ms'), findsOneWidget);
    await tester.tap(find.byTooltip('Check now'));
    await tester.pumpAndSettle();
    verify(() => handler.getSmartFailoverStatus(recheck: true)).called(1);
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: false));
    await tester.pump();
    verify(() => handler.getSmartFailoverStatus(recheck: false)).called(1);
    await tester.pump(const Duration(seconds: 3));
    verifyNoMoreInteractions(handler);
    expect(find.text('Automatic failover status'), findsNothing);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('status explains HTTP refusal and clears its timer on disposal', (
    tester,
  ) async {
    final container = _container();
    addTearDown(container.dispose);
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: true));
    final handler = _MockFailoverCore();
    when(
      () => handler.getSmartFailoverStatus(recheck: any(named: 'recheck')),
    ).thenAnswer(
      (_) async => {
        'state': 'unavailable',
        'running': true,
        'candidateCount': 2,
        'results': {
          'Japan': {'reason': 'service_refused:403'},
          'Singapore': {'reason': 'connection_failed'},
        },
      },
    );
    await tester.pumpWidget(
      _TestApp(
        container: container,
        child: SmartFailoverStatusItem(
          controller: CoreController.test(handler),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('HTTP 403'), findsOneWidget);
    expect(find.textContaining('Network/timeout 1'), findsOneWidget);
    expect(find.textContaining('200 ms'), findsOneWidget);
    verify(() => handler.getSmartFailoverStatus(recheck: false)).called(1);
    await tester.pumpWidget(const SizedBox());
    await tester.pump(const Duration(seconds: 3));
    verifyNoMoreInteractions(handler);
  });

  testWidgets('status displays core read failures', (tester) async {
    final container = _container();
    addTearDown(container.dispose);
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: true));
    final handler = _MockFailoverCore();
    when(
      () => handler.getSmartFailoverStatus(recheck: any(named: 'recheck')),
    ).thenAnswer((_) async => throw StateError('core disconnected'));
    await tester.pumpWidget(
      _TestApp(
        container: container,
        child: SmartFailoverStatusItem(
          controller: CoreController.test(handler),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('Cannot read the core status'), findsOneWidget);
    await tester.pumpWidget(const SizedBox());
    await tester.pump(const Duration(seconds: 3));
    expect(tester.takeException(), isNull);
  });

  test('old settings stay disabled and enabled settings round-trip', () {
    expect(AppSettingProps.fromJson({}).smartFailover, isFalse);
    expect(AppSettingProps.fromJson({}).smartFailoverMaxDelayMs, 200);
    const settings = AppSettingProps(
      smartFailover: true,
      smartFailoverMaxDelayMs: 350,
    );
    final restored = AppSettingProps.fromJson(
      jsonDecode(jsonEncode(settings.toJson())) as Map<String, dynamic>,
    );
    expect(restored.smartFailover, isTrue);
    expect(restored.smartFailoverMaxDelayMs, 350);
    const params = SetupParams(
      selectedMap: {},
      testUrl: 'https://example.com',
      smartFailover: true,
      smartFailoverMaxDelayMs: 350,
    );
    expect(params.toJson()['smart-failover'], isTrue);
    expect(SetupParams.fromJson(params.toJson()).smartFailover, isTrue);
    expect(params.toJson()['smart-failover-max-delay'], 350);
    expect(SetupParams.fromJson(params.toJson()).smartFailoverMaxDelayMs, 350);
  });

  test('global selection follows failover and restores the saved node', () {
    const profile = Profile(
      id: 1,
      autoUpdateDuration: Duration(days: 1),
      selectedMap: {'GLOBAL': 'Hong Kong 01', 'Proxy': 'Japan 01'},
    );
    final container = ProviderContainer(
      overrides: [currentProfileProvider.overrideWithValue(profile)],
    );
    addTearDown(container.dispose);
    expect(container.read(selectedMapProvider), profile.selectedMap);
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: true));
    expect(
      container.read(selectedMapProvider)['GLOBAL'],
      'FlClash Auto (non-HK)',
    );
    expect(container.read(selectedMapProvider)['Proxy'], 'Japan 01');
    expect(
      container.read(currentProfileProvider)?.selectedMap,
      profile.selectedMap,
    );
    container
        .read(appSettingProvider.notifier)
        .update((state) => state.copyWith(smartFailover: false));
    expect(container.read(selectedMapProvider), profile.selectedMap);
  });

  testWidgets('one switch enables and disables automatic failover', (
    tester,
  ) async {
    final container = _container();
    addTearDown(container.dispose);
    await tester.pumpWidget(
      _TestApp(container: container, child: const SmartFailoverItem()),
    );
    await tester.pumpAndSettle();
    expect(find.text('Automatic node failover'), findsOneWidget);
    expect(container.read(appSettingProvider).smartFailover, isFalse);
    await tester.tap(find.byType(Switch));
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartFailover, isTrue);
    await tester.tap(find.byType(Switch));
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartFailover, isFalse);
  });

  testWidgets('latency setting validates and saves the entered limit', (
    tester,
  ) async {
    final container = _container();
    addTearDown(container.dispose);
    await tester.pumpWidget(
      _TestApp(container: container, child: const SmartFailoverMaxDelayItem()),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Maximum usable latency'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextFormField), '0');
    await tester.tap(find.text('Submit'));
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartFailoverMaxDelayMs, 200);
    expect(find.byType(TextFormField), findsOneWidget);
    await tester.enterText(find.byType(TextFormField), '350');
    await tester.tap(find.text('Submit'));
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartFailoverMaxDelayMs, 350);
    expect(find.byType(TextFormField), findsNothing);
    expect(find.textContaining('350 ms'), findsOneWidget);
  });
}

ProviderContainer _container() => ProviderContainer(
  overrides: [
    viewSizeProvider.overrideWithBuild((_, _) => const Size(1200, 1000)),
  ],
);

class _TestApp extends StatelessWidget {
  const _TestApp({required this.container, required this.child});

  final ProviderContainer container;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return UncontrolledProviderScope(
      container: container,
      child: MaterialApp(
        navigatorKey: globalState.navigatorKey,
        locale: const Locale('en'),
        localizationsDelegates: const [
          AppLocalizations.delegate,
          GlobalMaterialLocalizations.delegate,
          GlobalWidgetsLocalizations.delegate,
          GlobalCupertinoLocalizations.delegate,
        ],
        builder: (context, child) {
          globalState.measure = Measure.of(context, 1);
          globalState.theme = CommonTheme.of(context, 1);
          return child!;
        },
        home: Scaffold(body: child),
      ),
    );
  }
}
