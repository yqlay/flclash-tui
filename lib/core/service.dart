import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/core/core.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/core.dart';
import 'package:flutter/foundation.dart' show visibleForTesting;

import 'interface.dart';
import 'transport.dart';

class CoreService extends CoreHandlerInterface {
  static CoreService? _instance;

  late final IPCCoreTransport _transport;

  final Duration _connectionTimeout;

  final String? _coreExecutablePath;

  Completer<bool> _shutdownCompleter = Completer();

  final Map<String, Completer> _callbackCompleterMap = {};

  Process? _process;

  late final Future<void> _initialization;

  factory CoreService() {
    _instance ??= CoreService._internal();
    return _instance!;
  }

  CoreService._internal()
    : _connectionTimeout = const Duration(seconds: 10),
      _coreExecutablePath = null {
    _transport = IPCCoreTransport(
      address: system.isWindows ? windowsPipeName : unixSocketPath,
    );
    _initialization = _initServer();
  }

  @visibleForTesting
  CoreService.test(
    this._transport, {
    Duration connectionTimeout = const Duration(seconds: 10),
    String? coreExecutablePath,
  }) : _connectionTimeout = connectionTimeout,
       _coreExecutablePath = coreExecutablePath {
    _initialization = _initServer();
  }

  @visibleForTesting
  Future<void> get initialized => _initialization;

  @visibleForTesting
  int get pendingInvocationCount => _callbackCompleterMap.length;

  Future<void> handleResult(ActionResult result) async {
    if (result.id?.isEmpty == true) {
      coreEventManager.sendEvent(CoreEvent.fromJson(result.data));
      return;
    }
    final completer = _callbackCompleterMap.remove(result.id);
    if (completer == null || completer.isCompleted) {
      return;
    }
    try {
      completer.complete(await parasResult(result));
    } catch (error, stackTrace) {
      completer.completeError(error, stackTrace);
    }
  }

  Future<void> _initServer() async {
    await _transport.init();

    _transport.onDisconnect = () {
      _handleInvokeCrashEvent();
      if (!_shutdownCompleter.isCompleted) {
        _shutdownCompleter.complete(true);
      }
    };

    _transport.dataStream
        .transform(uint8ListToListIntConverter)
        .transform(utf8.decoder)
        .listen(
          (data) async {
            try {
              final dataJson = await data.trim().commonToJSON<dynamic>();
              handleResult(ActionResult.fromJson(dataJson));
            } catch (e) {
              commonPrint.log(
                'Failed to parse transport data: $e',
                logLevel: LogLevel.error,
              );
            }
          },
          onError: (error) {
            commonPrint.log(
              'Transport data stream error: $error',
              logLevel: LogLevel.error,
            );
          },
        );
  }

  void _handleInvokeCrashEvent() {
    coreEventManager.sendEvent(
      const CoreEvent(type: CoreEventType.crash, data: 'core done'),
    );
  }

  Future<void> start() async {
    await _initialization;
    if (_process != null) {
      await shutdown(false);
    }
    if (system.isWindows && await system.checkIsAdmin()) {
      final isSuccess = await request.startCoreByHelper(_transport.address);
      if (isSuccess) {
        try {
          await _transport.connectionCompleter.future.timeout(
            _connectionTimeout,
          );
          return;
        } catch (error) {
          await request.stopCoreByHelper();
          _transport.disconnected();
          _handleInvokeCrashEvent();
          throw StateError('Privileged Core failed to connect: $error');
        }
      }
    }
    try {
      _process = await Process.start(_coreExecutablePath ?? appPath.corePath, [
        _transport.address,
      ]);
    } catch (e) {
      commonPrint.log(
        'Failed to start core process: $e',
        logLevel: LogLevel.error,
      );
      _handleInvokeCrashEvent();
      rethrow;
    }
    _process?.stdout.listen((_) {});
    _process?.stderr.listen((e) {
      final error = utf8.decode(e);
      if (error.isNotEmpty) {
        commonPrint.log(error, logLevel: LogLevel.warning);
      }
    });
    final process = _process;
    final connected = _transport.connectionCompleter.future;
    final exited = process!.exitCode.then<void>((exitCode) {
      throw StateError('Core exited before connecting (code $exitCode)');
    });
    try {
      await Future.any<void>([connected, exited]).timeout(_connectionTimeout);
    } catch (error) {
      process.kill();
      _process = null;
      _transport.disconnected();
      if (system.isWindows) {
        await request.stopCoreByHelper();
      }
      _handleInvokeCrashEvent();
      throw StateError('Core failed to connect: $error');
    }
  }

  @override
  FutureOr<bool> destroy() async {
    await shutdown(false);
    await _transport.close();
    return true;
  }

  Future<void> sendMessage(String message) async {
    await _transport.connectionCompleter.future;
    _transport.send(message);
  }

  @override
  Future<bool> shutdown(bool isUser) async {
    _shutdownCompleter = Completer();
    if (system.isWindows) {
      await request.stopCoreByHelper();
    }
    _transport.disconnected();
    final process = _process;
    _process = null;
    if (process != null) {
      process.kill();
      try {
        await process.exitCode.timeout(_connectionTimeout);
      } on TimeoutException {
        commonPrint.log(
          'Timed out while waiting for the Core process to exit.',
          logLevel: LogLevel.warning,
        );
      }
    }
    _clearCompleter();
    if (isUser) {
      return _shutdownCompleter.future.timeout(
        _connectionTimeout,
        onTimeout: () => false,
      );
    } else {
      return true;
    }
  }

  void _clearCompleter() {
    for (final completer in _callbackCompleterMap.values) {
      completer.safeCompleter(null);
    }
    _callbackCompleterMap.clear();
  }

  @override
  Future<String> preload() async {
    try {
      await start();
      return '';
    } catch (error) {
      return 'Failed to start Core: $error';
    }
  }

  @override
  Future<T?> invoke<T>({
    required ActionMethod method,
    dynamic data,
    Duration? timeout,
  }) async {
    final id = '${method.name}#${utils.id}';
    final completer = Completer<T?>();
    _callbackCompleterMap[id] = completer;
    try {
      await sendMessage(
        json.encode(Action(id: id, method: method, data: data)),
      );
      return await completer.future.withTimeout(
        timeout: timeout,
        onLast: () => completer.safeCompleter(null),
        tag: id,
        onTimeout: () => null,
      );
    } finally {
      _callbackCompleterMap.remove(id);
    }
  }

  @override
  Completer get completer => _transport.connectionCompleter;
}

final coreService = system.isDesktop ? CoreService() : null;
