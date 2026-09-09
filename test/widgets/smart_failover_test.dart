import 'dart:convert';

import 'package:fl_clash/l10n/l10n.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/config.dart';
import 'package:fl_clash/views/application_setting.dart';
import 'package:flutter/material.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
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

  testWidgets('one switch enables and disables automatic failover', (tester) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(
          locale: Locale('en'),
          localizationsDelegates: [
            AppLocalizations.delegate,
            GlobalMaterialLocalizations.delegate,
            GlobalWidgetsLocalizations.delegate,
            GlobalCupertinoLocalizations.delegate,
          ],
          home: Scaffold(body: SmartFailoverItem()),
        ),
      ),
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
}
