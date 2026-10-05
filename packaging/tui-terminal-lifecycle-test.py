#!/usr/bin/env python3
"""Exercise terminal loss without touching a host runtime (namespace-only)."""

import json
import os
import pty
import re
import signal
import subprocess
import sys
import time


def main():
    if os.environ.get('FLCLASH_EXIT_TEST_ISOLATED') != '1':
        raise SystemExit('Terminal lifecycle tests require an isolated runtime')
    binary, directory = sys.argv[1:]

    def cli(*args):
        return subprocess.check_output([binary, *args], text=True, timeout=5)

    def frontend_pids():
        return [int(value) for value in re.findall(r'^PID\s+(\d+)', cli('backend', 'clients'), re.M)]

    def alive(pid):
        try:
            # A zombie is no longer running; the parent still needs to reap it.
            with open(f'/proc/{pid}/stat', encoding='utf-8') as stream:
                return stream.read().rsplit(')', 1)[1].split()[0] != 'Z'
        except FileNotFoundError:
            return False

    cli('backend', 'start', '--directory', directory)
    backend_pid = json.loads(cli('status', '--json'))['backend_pid']
    for event in ('SIGHUP', 'terminal-close', 'parent-exit', 'two-frontends'):
        processes = []
        masters = []
        try:
            for _ in range(2 if event == 'two-frontends' else 1):
                master, slave = pty.openpty()
                command = [binary, 'tui', '--directory', directory]
                if event == 'parent-exit':
                    command = ['bash', '-c', '"$1" tui --directory "$2" <&0 & wait', 'terminal-parent', binary, directory]
                process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
                os.close(slave)
                processes.append(process)
                masters.append(master)
                os.write(master, b'\x1b]11;rgb:0000/0000/0000\x07\x1b[1;1R')
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                pids = frontend_pids()
                if len(pids) == len(processes):
                    break
                time.sleep(0.05)
            else:
                raise RuntimeError(f'{event}: TUI failed to register')
            if event == 'terminal-close':
                os.close(masters.pop())
            elif event == 'parent-exit':
                processes[0].kill()
            elif event == 'two-frontends':
                cli('exit')
            else:
                os.kill(pids[0], signal.SIGHUP)
            deadline = time.monotonic() + 8
            while any(alive(pid) for pid in pids) and time.monotonic() < deadline:
                time.sleep(0.05)
            if any(alive(pid) for pid in pids) or frontend_pids():
                raise RuntimeError(f'{event}: TUI process/session was not reclaimed')
            if event != 'two-frontends' and not alive(backend_pid):
                raise RuntimeError(f'{event}: terminal loss stopped the shared Backend')
            if event == 'two-frontends' and alive(backend_pid):
                raise RuntimeError('global exit left the Backend running')
            for process in processes:
                process.wait(timeout=5)
        finally:
            for master in masters:
                os.close(master)
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)
    print('SIGHUP/terminal-close/parent-exit/multi-frontend lifecycle tests passed')


if __name__ == '__main__':
    main()
