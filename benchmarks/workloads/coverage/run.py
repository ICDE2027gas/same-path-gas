#!/usr/bin/env python3
"""Run one controlled qualification or one complete bounded paid process on Linux."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time


def spec(path):
    path = path.resolve(strict=True)
    raw = path.read_bytes()
    return {'path': str(path), 'bytes': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()}


def save(path, value):
    with path.open('x', encoding='utf-8', newline='\n') as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write('\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path, help='Built benign-coverage.test')
    parser.add_argument('--artifacts', required=True, type=Path, help='controlled/materialize.py output artifacts.json')
    parser.add_argument('--phase', required=True, choices=('qualify', 'paid'))
    parser.add_argument('--method', choices=('S', 'C', 'V'))
    parser.add_argument('--qualification', type=Path, help='Complete matching qualification.json for paid mode')
    parser.add_argument('--cpu', type=int, default=0)
    parser.add_argument('--timeout', type=int, default=300)
    parser.add_argument('--out', required=True, type=Path, help='Fresh run directory')
    args = parser.parse_args()
    if os.name != 'posix' or not hasattr(os, 'wait4'):
        parser.error('Linux with wait4 and taskset is required')
    if args.cpu not in os.sched_getaffinity(0) or args.timeout <= 0:
        parser.error('Select an available CPU and a positive timeout')
    if (args.phase == 'qualify' and (args.method or args.qualification)) or (args.phase == 'paid' and not (args.method and args.qualification)):
        parser.error('qualify has no method/qualification; paid requires both')
    binary = args.binary.resolve(strict=True)
    artifacts = spec(args.artifacts)
    artifact = json.loads(Path(artifacts['path']).read_bytes())
    schedule = artifact['sources']['schedule']
    if spec(Path(schedule['path'])) != schedule:
        raise ValueError('Schedule identity differs')
    out = args.out.absolute()
    out.parent.resolve(strict=True)
    out.mkdir(mode=0o700)
    out = out.resolve(strict=True)
    manifest = {'schema': 'round1832-benign-input-v1', 'phase': args.phase, 'method': args.method or '',
                'artifacts': artifacts, 'schedule': schedule, 'qualification': spec(args.qualification) if args.qualification else None,
                'output_directory': str(out / 'native')}
    save(out / 'manifest.json', manifest)
    env = os.environ.copy()
    for key in ('GOGC', 'GOMEMLIMIT', 'GODEBUG'):
        env.pop(key, None)
    env.update({'GOMAXPROCS': '1', 'GETH_BENIGN_COVERAGE_MANIFEST': str(out / 'manifest.json'), 'GAS_CERTIFICATE_CPU': str(args.cpu)})
    taskset = shutil.which('taskset')
    if not taskset:
        raise RuntimeError('taskset is required')
    argv = [taskset, '--cpu-list', str(args.cpu), str(binary), '-test.run', '^TestBenignCoverage$', '-test.count=1', '-test.timeout', str(args.timeout) + 's']
    started = time.monotonic_ns()
    timed_out = False
    with (out / 'stdout.log').open('xb') as stdout, (out / 'stderr.log').open('xb') as stderr:
        child = subprocess.Popen(argv, stdout=stdout, stderr=stderr, env=env, start_new_session=True)
        deadline = time.monotonic() + args.timeout + 5
        try:
            while True:
                pid, status, usage = os.wait4(child.pid, os.WNOHANG)
                if pid:
                    break
                if time.monotonic() >= deadline:
                    timed_out = True
                    os.killpg(child.pid, signal.SIGKILL)
                    pid, status, usage = os.wait4(child.pid, 0)
                    break
                time.sleep(0.05)
        except BaseException:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            pid, status, usage = os.wait4(child.pid, 0)
            child.returncode = os.waitstatus_to_exitcode(status)
            raise
        child.returncode = os.waitstatus_to_exitcode(status)
    result = {'schema': 'gas-certificate-controlled-process-v1', 'complete': False, 'phase': args.phase, 'method': args.method,
              'binary': spec(binary), 'argv': argv, 'exitcode': child.returncode, 'timed_out': timed_out,
              'whole_process_wall_ns': time.monotonic_ns() - started,
              'whole_process_user_ns': round(usage.ru_utime * 1e9), 'whole_process_system_ns': round(usage.ru_stime * 1e9),
              'whole_process_maximum_rss_kib': usage.ru_maxrss, 'direct_child_reaped': True,
              'native_receipt': None, 'recorded_paper_result': False}
    receipt_path = out / 'native/receipt.json'
    if receipt_path.is_file():
        result['native_receipt'] = spec(receipt_path)
        native = json.loads(receipt_path.read_bytes())
        result['complete'] = child.returncode == 0 and not timed_out and native['complete'] is True and native['inputs_preserved'] is True
    save(out / 'process.json', result)
    print(json.dumps(result))
    if not result['complete']:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
