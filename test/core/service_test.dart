import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:fl_clash/core/service.dart';
import 'package:fl_clash/core/transport.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/core.dart';
import 'package:flutter_test/flutter_test.dart';

class FakeCoreTransport extends IPCCoreTransport {
  FakeCoreTransport({bool connectedInitially = true})
    : super(address: 'test-core') {
    if (connectedInitially) {
      connected.complete();
    }
  }

  final StreamController<Uint8List> controller = StreamController<Uint8List>();
  final Completer<void> connected = Completer<void>();
  final List<String> messages = [];

  @override
  Completer<void> get connectionCompleter => connected;

  @override
  Stream<Uint8List> get dataStream => controller.stream;

  @override
  Future<void> init() async {}

  @override
  void send(String message) {
    messages.add(message);
  }

  @override
  void disconnected() {}

  @override
  Future<void> close() async {
    await controller.close();
  }
}

void main() {
  late FakeCoreTransport transport;
  late CoreService service;

  setUp(() async {
    transport = FakeCoreTransport();
    service = CoreService.test(transport);
    await service.initialized;
  });

  tearDown(() async {
    await service.destroy();
  });

  test(
    'completed invocations are removed from the pending callback map',
    () async {
      final invocation = service.invoke<String>(method: ActionMethod.getMemory);
      await Future<void>.delayed(Duration.zero);
      final action = Action.fromJson(
        json.decode(transport.messages.single) as Map<String, dynamic>,
      );
      expect(service.pendingInvocationCount, 1);

      await service.handleResult(
        ActionResult(id: action.id, method: action.method, data: '2048'),
      );

      expect(await invocation, '2048');
      expect(service.pendingInvocationCount, 0);
    },
  );

  test(
    'timed out invocations are removed from the pending callback map',
    () async {
      final result = await service.invoke<String>(
        method: ActionMethod.getMemory,
        timeout: const Duration(milliseconds: 5),
      );

      expect(result, isNull);
      expect(service.pendingInvocationCount, 0);
    },
  );

  test('shutdown completes and clears every pending invocation', () async {
    final first = service.invoke<String>(method: ActionMethod.getMemory);
    final second = service.invoke<String>(method: ActionMethod.getTraffic);
    await Future<void>.delayed(Duration.zero);
    expect(service.pendingInvocationCount, 2);

    await service.shutdown(false);

    expect(await first, isNull);
    expect(await second, isNull);
    expect(service.pendingInvocationCount, 0);
  });

  test('user shutdown does not wait forever for a disconnect event', () async {
    final timeoutTransport = FakeCoreTransport();
    final timeoutService = CoreService.test(
      timeoutTransport,
      connectionTimeout: const Duration(milliseconds: 5),
    );
    await timeoutService.initialized;

    expect(await timeoutService.shutdown(true), isFalse);
    await timeoutService.destroy();
  });

  test(
    'preload reports process launch failures instead of false success',
    () async {
      final failingTransport = FakeCoreTransport(connectedInitially: false);
      final failingService = CoreService.test(
        failingTransport,
        coreExecutablePath: '/definitely/missing/flclash-core',
        connectionTimeout: const Duration(milliseconds: 20),
      );
      await failingService.initialized;

      final result = await failingService.preload();

      expect(result, contains('Failed to start Core'));
      await failingService.destroy();
    },
  );

  test(
    'preload reports a Core process that exits before IPC connects',
    () async {
      final failingTransport = FakeCoreTransport(connectedInitially: false);
      final failingService = CoreService.test(
        failingTransport,
        coreExecutablePath: '/bin/true',
        connectionTimeout: const Duration(seconds: 1),
      );
      await failingService.initialized;

      final result = await failingService.preload();

      expect(result, contains('Core exited before connecting'));
      await failingService.destroy();
    },
  );
}
