#!/usr/bin/env python3
"""Qualify all 116 historical inputs and run the fixed three-method paid schedule."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time

HERE = Path(__file__).resolve().parent
TESTS = {
    'stock-certificate': ('GETH_CERTIFICATE_FIXTURE_MANIFEST', 'TestGasCertificateFixtureCohort', 'round1719-gas-functional-input-v1'),
    'three-method': ('GETH_GANACHE_CONTROL_FIXTURE_MANIFEST', 'TestGasGanacheControlFixtureCohort', 'round1743-three-method-functional-input-v1'),
    'paid': ('GETH_THREE_METHOD_PAID_COST_MANIFEST', 'TestGasThreeMethodPaidCostCohort', 'round1749-three-method-paid-input-v1'),
}


def spec(path):
    path = Path(path)
    if not path.is_absolute() or path.resolve(strict=True) != path or not path.is_file():
        raise ValueError('Canonical absolute regular file required: ' + str(path))
    digest = hashlib.sha256()
    count = 0
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1 << 20), b''):
            count += len(block)
            digest.update(block)
    return {'path': str(path), 'bytes': count, 'sha256': digest.hexdigest()}


def save(path, value):
    with path.open('x', encoding='utf-8', newline='\n') as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write('\n')
    return spec(path)


def read(filespec):
    if spec(Path(filespec['path'])) != filespec:
        raise ValueError('Content identity changed: ' + filespec['path'])
    return json.loads(Path(filespec['path']).read_bytes())


def prepare_capture(inputs, out, binary):
    corpus_spec = spec(HERE / 'corpus.json')
    expected_spec = spec(HERE / 'FILES.expected.json')
    corpus = read(corpus_spec)
    expected = read(expected_spec)
    if corpus['schema'] != 'gas-certificate-historical116-corpus-v1' or corpus['count'] != 116 or len(corpus['raw_sends']) != 116:
        raise ValueError('Complete declared corpus required')
    if not all(corpus[key] is True for key in ('complete', 'original_assertions_passed', 'provider_closed')) or corpus['errors']:
        raise ValueError('Captured source did not complete')
    if len(expected['files']) != 233 or expected['count'] != 116 or expected['total_bytes'] != 11691660:
        raise ValueError('Complete 233-file historical payload inventory required')
    inputs = inputs.resolve(strict=True)
    files = {}
    for relative, identity in expected['files'].items():
        name = Path(relative)
        if name.is_absolute() or '..' in name.parts:
            raise ValueError('Payload path must be relative to inputs')
        path = inputs / name
        actual = spec(path)
        if {key: actual[key] for key in ('bytes', 'sha256')} != identity:
            raise ValueError('Captured payload differs: ' + relative)
        files[relative] = actual
    receipt = {key: copy.deepcopy(corpus[key]) for key in (
        'complete', 'errors', 'source_pin', 'fork', 'original_assertions_passed', 'provider_closed', 'cases', 'expected_cases', 'raw_sends')}
    receipt.update({'schema': 'round1714-uniswap-materialization-v1', 'files': list(files.values()),
                    'inputs': [corpus_spec, expected_spec], 'source_receipt_identity': corpus['source_receipt_identity'],
                    'portable_capture_adapter': True, 'captured_payloads_unchanged': True})
    for row in receipt['raw_sends']:
        for key in ('input', 'result'):
            original = row[key]
            actual = files.get(original['path'])
            if not actual or any(original[item] != actual[item] for item in ('bytes', 'sha256')):
                raise ValueError('Send/input/result association differs')
            row[key] = actual
    return save(out / 'materialization.json', receipt)


def run_stage(stage, binary, binary_spec, manifest, out, cpu, timeout):
    variable, test, schema = TESTS[stage]
    manifest = dict(manifest, schema=schema, test_binary=binary_spec, output_directory=str(out / stage))
    manifest_spec = save(out / (stage + '.manifest.json'), manifest)
    environment = os.environ.copy()
    for key in ('GOGC', 'GOMEMLIMIT', 'GODEBUG', 'GETH_CERTIFICATE_FIXTURE_MANIFEST',
                'GETH_GANACHE_CONTROL_FIXTURE_MANIFEST', 'GETH_THREE_METHOD_PAID_COST_MANIFEST'):
        environment.pop(key, None)
    environment.update({'GOMAXPROCS': '1', variable: manifest_spec['path']})
    argv = [shutil.which('taskset'), '--cpu-list', str(cpu), str(binary), '-test.run', '^' + test + '$',
            '-test.count=1', '-test.timeout', str(timeout) + 's']
    result = {'schema': 'gas-certificate-historical116-process-v1', 'stage': stage, 'complete': False,
              'binary': binary_spec, 'manifest': manifest_spec, 'cpu': cpu, 'argv': argv,
              'timed_out': False, 'direct_child_reaped': False, 'recorded_paper_result': False}
    started = time.monotonic_ns()
    try:
        with (out / (stage + '.stdout.log')).open('xb') as stdout, (out / (stage + '.stderr.log')).open('xb') as stderr:
            child = subprocess.Popen(argv, stdout=stdout, stderr=stderr, env=environment, start_new_session=True)
            deadline = time.monotonic() + timeout + 5
            try:
                while True:
                    pid, status, usage = os.wait4(child.pid, os.WNOHANG)
                    if pid:
                        break
                    if time.monotonic() >= deadline:
                        result['timed_out'] = True
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
                result['direct_child_reaped'] = True
                raise
            child.returncode = os.waitstatus_to_exitcode(status)
            result.update({'exitcode': child.returncode, 'direct_child_reaped': True,
                           'whole_process_user_ns': round(usage.ru_utime * 1e9),
                           'whole_process_system_ns': round(usage.ru_stime * 1e9),
                           'whole_process_maximum_rss_kib': usage.ru_maxrss})
        summary_path = out / stage / 'summary.json'
        if summary_path.is_file():
            summary_spec = spec(summary_path)
            summary = read(summary_spec)
            result['summary'] = summary_spec
            result['complete'] = child.returncode == 0 and not result['timed_out'] and summary['complete'] is True and summary['inputs_preserved'] is True and not summary['errors']
        result['binary_preserved'] = spec(binary) == binary_spec
        result['manifest_preserved'] = spec(Path(manifest_spec['path'])) == manifest_spec
        result['complete'] = result['complete'] and result['binary_preserved'] and result['manifest_preserved']
    finally:
        result['whole_process_wall_ns'] = time.monotonic_ns() - started
        save(out / (stage + '.process.json'), result)
    if not result['complete']:
        raise RuntimeError(stage + ' failed; complete logs and generated rows retained')
    return result['summary']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True, help='Built historical116.test with historical runtime and corpus overlay')
    parser.add_argument('--inputs', type=Path, help='Captured payload directory containing transactions/ and events.jsonl')
    parser.add_argument('--phase', choices=('qualify', 'paid', 'all'), default='qualify')
    parser.add_argument('--qualification', type=Path, help='Successful three-method/summary.json from this same binary and unchanged inputs')
    parser.add_argument('--cpu', type=int, required=True)
    parser.add_argument('--timeout', type=int, default=600, help='Maximum seconds for each native process')
    parser.add_argument('--out', type=Path, required=True, help='Fresh output directory')
    args = parser.parse_args()
    if os.name != 'posix' or not hasattr(os, 'wait4') or not Path('/proc/self/status').is_file() or not shutil.which('taskset'):
        parser.error('Linux, taskset and wait4 are required')
    if args.cpu not in os.sched_getaffinity(0) or args.timeout <= 0:
        parser.error('Choose one available CPU and a positive timeout')
    if args.phase in ('qualify', 'all') and (not args.inputs or args.qualification):
        parser.error('qualify/all requires --inputs and generates its reference chain')
    if args.phase == 'paid' and (not args.qualification or args.inputs):
        parser.error('paid requires --qualification and reuses its content-bound input/reference chain')
    binary = args.binary.resolve(strict=True)
    binary_spec = spec(binary)
    out = args.out.absolute()
    out.parent.resolve(strict=True)
    out.mkdir(mode=0o700)
    out = out.resolve(strict=True)
    receipt = {'schema': 'gas-certificate-historical116-run-v1', 'phase': args.phase, 'complete': False,
               'binary': binary_spec, 'cpu': args.cpu, 'stages': {}, 'errors': [], 'recorded_paper_result': False}
    try:
        if args.phase in ('qualify', 'all'):
            materialized = prepare_capture(args.inputs, out, binary_spec)
            receipt['materialization'] = materialized
            stock = run_stage('stock-certificate', binary, binary_spec, {'materialization_receipt': materialized}, out, args.cpu, args.timeout)
            receipt['stages']['stock-certificate'] = stock
            qualified = run_stage('three-method', binary, binary_spec, {'functional_summary': stock}, out, args.cpu, args.timeout)
            receipt['stages']['three-method'] = qualified
        else:
            qualified = spec(args.qualification.resolve(strict=True))
            reference = read(qualified)
            if reference.get('test_binary') != binary_spec or reference.get('complete') is not True or reference.get('errors'):
                raise ValueError('Paid requires a successful whole116 qualification from this same binary')
        receipt['qualification'] = qualified
        if args.phase in ('paid', 'all'):
            receipt['stages']['paid'] = run_stage('paid', binary, binary_spec, {'functional_summary': qualified}, out, args.cpu, args.timeout)
        receipt['complete'] = True
    except BaseException as error:
        receipt['errors'].append(type(error).__name__ + ': ' + str(error))
        raise
    finally:
        save(out / 'run.json', receipt)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
