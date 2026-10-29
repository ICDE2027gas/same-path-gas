#!/usr/bin/env python3
"""Verify fixed inputs, clone state, and run one RPC qualification or measured unit.

Paid units use the preserved serial/concurrent/replay controllers unchanged.
The default qualification covers the complete population once. --smoke is a
small functional subset and never produces a paid-eligible qualification.
"""
import argparse
import base64
import copy
import csv
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import time
import traceback


HERE = Path(__file__).resolve().parent
SMOKE_POSITIONS = {'E': [0, 2, 8], 'A': [0, 3, 6, 15]}


def need(value, message):
    if not value:
        raise ValueError(message)


def canonical(value):
    p = Path(value).absolute()
    need(p.resolve(strict=True) == p, 'Use canonical absolute paths without symlinks: ' + str(p))
    return p


def identity(p):
    p = canonical(p)
    h = hashlib.sha256()
    size = 0
    with p.open('rb') as f:
        for part in iter(lambda: f.read(1 << 20), b''):
            size += len(part)
            h.update(part)
    return {'path': str(p), 'bytes': size, 'sha256': h.hexdigest()}


def same_bytes(p, expected):
    actual = identity(p)
    need(all(actual[k] == expected[k] for k in ('bytes', 'sha256')), 'Input bytes/hash differ: ' + str(p))
    return actual


def read(p):
    return json.loads(Path(p).read_bytes())


def save(p, value):
    with Path(p).open('x', encoding='utf-8', newline='\n') as f:
        json.dump(value, f, indent=2, sort_keys=True, allow_nan=False)
        f.write('\n')
    return identity(p)


def copied(source, destination, executable=False):
    source = canonical(source)
    need(not destination.exists(), 'Fresh copy destination required')
    shutil.copyfile(source, destination)
    destination.chmod(0o700 if executable else 0o600)
    return same_bytes(destination, identity(source))


def load_module(name, source):
    spec = importlib.util.spec_from_file_location(name, source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def state_source(given, cohort, node_name):
    # Accept a cohort directory, a directory containing E/A, or an archive root.
    candidates = [given, given / 'node', given / cohort, given / cohort / 'node',
                  given / 'states' / cohort, given / 'states' / cohort / 'node',
                  given / 'workflow-state' / cohort, given / 'workflow-state' / cohort / 'node']
    found = []
    for p in candidates:
        if (p / node_name / 'chaindata').is_dir():
            q = canonical(p)
            if q not in found:
                found.append(q)
    need(len(found) == 1, '--state-dir must identify exactly one unpacked ' + cohort + ' state directory')
    return found[0]


def verify_state(directory, expected):
    node = expected['node_name']
    actual_files = set()
    for sub in ('chaindata', 'triedb'):
        base = directory / node / sub
        need(base.is_dir() and canonical(base) == base, 'Expected closed state directory: ' + str(base))
        for p in base.rglob('*'):
            need(not p.is_symlink(), 'State symlinks are not allowed')
            if p.is_file():
                actual_files.add(p.relative_to(directory).as_posix())
    need(actual_files == set(expected['files']), 'State DB file set differs; dirty/incomplete state is not reusable')
    for name, spec in expected['files'].items():
        rel = Path(name)
        need(not rel.is_absolute() and '..' not in rel.parts and rel.parts[0] == node, 'Unsafe state manifest member')
        same_bytes(directory / rel, spec)
    return {name: identity(directory / name) for name in sorted(expected['files'])}


def clone_state(source, destination, expected):
    verify_state(source, expected)
    destination.mkdir(mode=0o700)
    for name, expected_file in expected['files'].items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        copied(source / name, target)
        same_bytes(target, expected_file)
    verify_state(destination, expected)
    verify_state(source, expected)


def cpu_topology(cpu):
    p = Path('/sys/devices/system/cpu') / ('cpu' + str(cpu))
    return (int((p / 'topology/physical_package_id').read_text()),
            int((p / 'topology/core_id').read_text()),
            tuple(sorted(x.name for x in p.glob('node[0-9]*'))))


def parse_cpus(value):
    result = [int(x) for x in value.split(',')]
    need(result and len(result) == len(set(result)) and min(result) >= 0, 'CPU lists must contain distinct nonnegative integers')
    return result


def cpu_selection(args):
    allowed = sorted(os.sched_getaffinity(0))
    if args.smoke and not args.server_cpus and not args.client_cpus:
        need(allowed, 'An available CPU is required')
        # The functional smoke checks RPC behavior in the controller process.
        return [allowed[0]], [allowed[0]]
    concurrent = args.mode in ('concurrent', 'replay')
    width = 4 if concurrent else 1
    if args.server_cpus or args.client_cpus:
        need(args.server_cpus and args.client_cpus, 'Supply both --server-cpus and --client-cpus')
        server, client = parse_cpus(args.server_cpus), parse_cpus(args.client_cpus)
    else:
        groups = {}
        for cpu in allowed:
            package, core, nodes = cpu_topology(cpu)
            groups.setdefault((package, nodes), {}).setdefault(core, cpu)
        selected = next((list(g.values())[:2 * width] for g in groups.values() if len(g) >= 2 * width), None)
        need(selected, 'Insufficient distinct physical cores on one socket/NUMA node; specify CPU lists')
        server, client = selected[:width], selected[width:]
    need(len(server) == len(client) == width, 'This mode requires ' + str(width) + ' server/client CPUs each')
    selected = server + client
    need(len(set(selected)) == 2 * width and set(selected) <= set(allowed), 'CPU masks must be disjoint and available')
    topo = [cpu_topology(c) for c in selected]
    need(len({(t[0], t[1]) for t in topo}) == 2 * width, 'Select distinct physical cores, excluding SMT siblings')
    need(len({(t[0], t[2]) for t in topo}) == 1 and all(len(t[2]) == 1 for t in topo), 'Select one socket and one NUMA node')
    return server, client


def available_ports(http_port, auth_port):
    held = []
    try:
        for desired in (http_port, auth_port):
            need(desired == 0 or 1024 <= desired <= 65535, 'Ports must be zero (automatic) or 1024..65535')
            sock = socket.socket()
            sock.bind(('127.0.0.1', desired))
            held.append(sock)
        return tuple(s.getsockname()[1] for s in held)
    finally:
        for sock in held:
            sock.close()


class Run:
    def __init__(self, args):
        self.args = args
        self.stages = []
        self.created_output = False
        self.repository = canonical(args.repository or HERE.parents[2])
        self.workloads = self.repository / 'benchmarks/workloads'
        self.expected = read(self.workloads / 'uniswap/STATES.expected.json')['cohorts'][args.cohort]
        origins_path = self.workloads / 'uniswap/ORIGINS.json'
        self.origins = read(origins_path)['cohorts'][args.cohort]
        incoming = state_source(canonical(args.state_dir), args.cohort, self.expected['node_name'])
        verify_state(incoming, self.expected)
        self.server_cpus, self.client_cpus = cpu_selection(args)
        root = Path(args.output).absolute()
        need(root.parent.resolve(strict=True) == root.parent and not root.exists(), '--output must be fresh under a canonical existing parent')
        need(not root.is_relative_to(incoming) and not incoming.is_relative_to(root), 'Output must be separate from the preserved input state')
        root.mkdir(mode=0o700)
        self.root = root
        self.created_output = True
        for name in ('inputs', 'bin', 'states', 'clones', 'stages', 'harness'):
            (root / name).mkdir(mode=0o700)
        self.source = root / 'states' / args.cohort
        clone_state(incoming, self.source, self.expected)
        self.source_proofs = []
        for name in ('ORIGINS.json', 'STATES.expected.json'):
            self.source_proofs.append(copied(self.workloads / 'uniswap' / name, root / 'inputs' / name))
        self.target = copied(canonical(args.binary), root / 'bin' / 'target-rpc', executable=True)
        self.pristine = self.target if args.variant == 'P' else None
        if args.pristine_binary:
            self.pristine = copied(canonical(args.pristine_binary), root / 'bin' / 'pristine-rpc', executable=True)
        # Copy controller sources into the workspace so their path checks can
        # enforce containment beneath ROOT.
        for mode in ('serial', 'concurrent', 'replay'):
            (root / 'harness' / mode).mkdir(mode=0o700)
            for name in ('client.py', 'unit.py'):
                self.source_proofs.append(copied(HERE / mode / name, root / 'harness' / mode / name))
        self.source_proofs.append(copied(HERE / 'lifecycle.py', root / 'harness/lifecycle.py'))
        portable_context = self.workloads / 'uniswap' / (args.cohort + '.context.json')
        portable_population = self.workloads / 'uniswap' / (args.cohort + '.population.json')
        same_bytes(portable_context, self.origins['portable_context'])
        same_bytes(portable_population, self.origins['portable_population'])
        context = read(portable_context)
        population = read(portable_population)
        self.original_context = copy.deepcopy(context)
        context['original_node_directory'] = str(self.source)
        self.context = save(root / 'inputs/context.json', context)
        population['context'] = self.context
        self.population = save(root / 'inputs/population.json', population)
        need(read(root / 'inputs/context.json')['headers'] == self.original_context['headers'], 'Header bytes must remain unchanged')
        need(population['entries'] == read(portable_population)['entries'], 'Every request must remain unchanged')
        self.binding = save(root / 'inputs/binding.json', {'schema': 'gas-certificate-portable-binding-v1',
            'cohort': args.cohort, 'original_portable_context': identity(portable_context),
            'original_portable_population': identity(portable_population), 'context': self.context,
            'population': self.population, 'state_file_identities': self.expected['files'],
            'only_context_change': 'original_node_directory', 'only_population_change': 'context FileSpec',
            'headers_and_entries_unchanged': True})
        self.source_proofs.append(self.binding)
        os.environ.update({'GAS_CERTIFICATE_BENCH_ROOT': str(root),
            'GAS_CERTIFICATE_SERIAL_SERVER_CPU': str(self.server_cpus[0]),
            'GAS_CERTIFICATE_SERIAL_CLIENT_CPU': str(self.client_cpus[0]),
            'GAS_CERTIFICATE_SERVER_CPUS': ','.join(map(str, self.server_cpus)),
            'GAS_CERTIFICATE_CLIENT_CPUS': ','.join(map(str, self.client_cpus))})
        self.lifecycle = load_module('gas_certificate_lifecycle', root / 'harness/lifecycle.py')
        self.serial = load_module('gas_certificate_serial_unit', root / 'harness/serial/unit.py')
        save(root / 'invocation.json', {'schema': 'gas-certificate-rpc-invocation-v1', 'argv': sys.argv,
            'mode': args.mode, 'variant': args.variant, 'cohort': args.cohort, 'smoke': args.smoke,
            'declared_source_variant': args.source_variant, 'recorded_paper_result': False,
            'target_binary': self.target, 'pristine_binary': self.pristine,
            'server_cpus': self.server_cpus, 'client_cpus': self.client_cpus,
            'context': self.context, 'population': self.population, 'binding': self.binding})

    def manifest(self, label, binary, variant, purpose, qualification=None):
        stage = self.root / 'stages' / label
        stage.mkdir(mode=0o700)
        clone = self.root / 'clones' / label
        clone_state(self.source, clone, self.expected)
        clone_receipt = save(stage / 'clone.json', {'schema': 'round1773-independent-clone-v1',
            'complete': True, 'source_data_directory': str(self.source), 'clone_data_directory': str(clone),
            'independent_closed_full_clone': True, 'verified_files': self.expected['files'],
            'excluded_non_state_files': self.expected.get('excluded', [])})
        hp, ap = available_ports(self.args.http_port, self.args.auth_port)
        manifest = {'schema': 'round1789-server-input-v1', 'purpose': purpose, 'cohort': self.args.cohort,
            'variant': variant, 'server_binary': binary, 'context': self.context, 'population': self.population,
            'qualification': qualification, 'clone_receipt': clone_receipt, 'source_proofs': self.source_proofs,
            'clone_data_directory': str(clone), 'output_directory': str(stage / 'server'),
            'http_port': hp, 'auth_port': ap}
        manifest_spec = save(stage / 'server.json', manifest)
        return stage, manifest, manifest_spec

    def unit(self, label, binary, variant, purpose, baseline=None, qualification=None, mode='serial'):
        print(json.dumps({'event': 'stage_started', 'stage': label, 'purpose': purpose,
                          'cohort': self.args.cohort, 'variant': variant}), flush=True)
        stage, server, server_spec = self.manifest(label, binary, variant, purpose, qualification)
        concurrent = mode in ('concurrent', 'replay')
        if concurrent:
            module = load_module('gas_certificate_' + mode, self.root / 'harness' / mode / 'unit.py')
            warm, measured = (4, 1024) if self.args.cohort == 'E' else (1, 64)
            clients = []
            for i in range(4):
                client = {'schema': 'round1810-client-input-v1', 'server_manifest': server_spec,
                    'client_id': i, 'worker_count': 4, 'client_cpu': self.client_cpus[i],
                    'warmup_passes': warm, 'measured_passes': measured, 'baseline_qualification': None,
                    'output_directory': str(stage / ('client-' + str(i))), 'response_body_cap': 8 << 20,
                    'raw_response_cap_bytes': 16 << 20, 'http_timeout_seconds': 120}
                clients.append(save(stage / ('client-' + str(i) + '.json'), client)['path'])
            result = module.run_unit(server_spec['path'], clients, str(stage / 'unit'), self.lifecycle,
                timeout_seconds=self.args.timeout, inherited_lock_fd=self.args.lock_fd)
        else:
            warm, measured = ((16, 4096) if self.args.cohort == 'E' else (1, 256)) if purpose == 'paid' else (0, 0)
            client = {'schema': 'round1789-client-input-v1', 'server_manifest': server_spec,
                'baseline_qualification': baseline, 'warmup_passes': warm, 'measured_passes': measured,
                'output_directory': str(stage / 'client'), 'response_body_cap': 8 << 20, 'http_timeout_seconds': 120}
            client_spec = save(stage / 'client.json', client)
            result = self.serial.run_unit(server_spec['path'], client_spec['path'], str(stage / 'unit'),
                self.lifecycle, timeout_seconds=self.args.timeout, inherited_lock_fd=self.args.lock_fd)
        self.stages.append({'label': label, 'receipt': identity(result['receipt']), 'complete': result['complete']})
        need(result['complete'], 'Stage failed; complete logs retained: ' + label)
        verify_state(self.source, self.expected)
        print(json.dumps({'event': 'stage_completed', 'stage': label,
                          'receipt': result['receipt']}), flush=True)
        return result

    def smoke(self):
        print(json.dumps({'event': 'stage_started', 'stage': 'smoke',
                          'cohort': self.args.cohort, 'variant': self.args.variant}), flush=True)
        stage, server_manifest, server_spec = self.manifest('smoke', self.target, self.args.variant, 'qualification')
        module = self.serial
        child = control = connection = None
        result = {'schema': 'gas-certificate-rpc-smoke-v1', 'complete': False, 'subset_only': True,
            'complete_population_qualification': False, 'performance_measured': False,
            'cohort': self.args.cohort, 'variant': self.args.variant, 'positions': SMOKE_POSITIONS[self.args.cohort],
            'server_manifest': server_spec, 'requests': [], 'errors': []}
        expected_path = self.repository / 'benchmarks/results/workflow-returned-estimates.csv'
        result['expected_results'] = identity(expected_path)
        with expected_path.open(newline='', encoding='utf-8') as f:
            expected = [r for r in csv.DictReader(f) if r['workflow'] == self.args.cohort
                        and r['method'] == ('C' if self.args.variant == 'V' else self.args.variant)]
        population = read(self.population['path'])
        context = read(self.context['path'])
        deadline = time.monotonic() + self.args.timeout
        with (stage / 'server.stderr.log').open('xb') as stderr, (stage / 'server.control.jsonl').open('xb') as journal:
            try:
                env = os.environ.copy()
                for k in ('GOGC', 'GOMEMLIMIT', 'GODEBUG'):
                    env.pop(k, None)
                env['GOMAXPROCS'] = '1'
                passed = () if self.args.lock_fd is None else (self.args.lock_fd,)
                process = subprocess.Popen([shutil.which('taskset'), '-c', str(self.server_cpus[0]),
                    self.target['path'], '--manifest', server_spec['path']], stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE, stderr=stderr, env=env, start_new_session=True, pass_fds=passed, bufsize=0)
                child = module.Wait4Child(process)
                control = module.Control(process, child, journal)
                ready = control.receive('READY', deadline)
                need(ready['pid'] == child.pid and all(ready[k] == server_manifest[k]
                    for k in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population')), 'Exact smoke READY')
                connection = http.client.HTTPConnection('127.0.0.1', server_manifest['http_port'], timeout=120)

                def rpc(request):
                    need(time.monotonic() < deadline, 'Smoke request deadline')
                    connection.timeout = min(120, max(1, deadline - time.monotonic()))
                    if connection.sock is not None:
                        connection.sock.settimeout(connection.timeout)
                    connection.request('POST', '/', body=json.dumps(request).encode(), headers={'Content-Type': 'application/json'})
                    response = connection.getresponse()
                    raw = response.read((8 << 20) + 1)
                    need(response.status == 200 and len(raw) <= 8 << 20, 'Smoke HTTP body/status')
                    value = json.loads(raw)
                    need(value.get('id') == request['id'] and value.get('jsonrpc') == '2.0'
                         and (('result' in value) != ('error' in value)), 'Smoke RPC envelope')
                    result['requests'].append({'request': request, 'http_status': response.status,
                        'raw_response_base64': base64.b64encode(raw).decode()})
                    return {k: v for k, v in value.items() if k != 'id'}

                def head():
                    value = rpc({'jsonrpc': '2.0', 'id': 'smoke-head', 'method': 'eth_getBlockByNumber', 'params': ['latest', False]})['result']
                    want = context['headers'][-1]
                    need(all(value[k] == want[k] for k in ('number', 'hash', 'stateRoot')), 'Exact fixed smoke head')

                head()
                for position in SMOKE_POSITIONS[self.args.cohort]:
                    request = copy.deepcopy(population['entries'][position]['estimate_request'])
                    value = rpc(request)
                    reference = next(r for r in expected if int(r['position']) == position)
                    if reference['returned_gas_hex']:
                        need(value.get('result') == reference['returned_gas_hex'], 'Expected smoke estimate budget')
                        call = copy.deepcopy(request)
                        call['method'] = 'eth_call'
                        call['params'][0]['gas'] = value['result']
                        answer = rpc(call)
                        need('result' in answer and isinstance(answer['result'], str)
                             and answer['result'].startswith('0x'), 'Returned-budget call succeeds')
                        raw = bytes.fromhex(answer['result'][2:])
                        need(len(raw) == int(reference['call_result_bytes']) and hashlib.sha256(raw).hexdigest() == reference['call_result_sha256'], 'Exact smoke call return bytes')
                    else:
                        need(value.get('error') == {'code': int(reference['error_code']), 'message': reference['error_message'], 'data': reference['error_data']}, 'Exact smoke rejection payload')
                head()
                control.send('STOP')
                closed = control.receive('CLOSED', deadline)
                need(closed['context_unchanged'] and child.wait(timeout=max(.01, deadline - time.monotonic())) == 0, 'Smoke STOP and exit0')
                receipt = read(stage / 'server/receipt.json')
                need(receipt['complete'] and receipt['inputs_preserved'] and receipt['stack_closed'] and not receipt['errors'], 'Complete smoke server lifecycle')
                need(receipt['context_before'] == receipt['context_after_readiness'] == receipt['context_after'], 'Smoke canonical context unchanged')
                need(receipt['history_opening']['ready'] and receipt['history_opening']['complete'], 'All historical roots ready')
                result['server_receipt'] = identity(stage / 'server/receipt.json')
                result['complete'] = True
            except (Exception, KeyboardInterrupt) as error:
                result['errors'].append({'error': str(error), 'traceback': traceback.format_exc()})
            finally:
                if connection is not None:
                    connection.close()
                if child is not None:
                    try:
                        result['retirement'] = self.lifecycle.retire(child)
                        result['whole_server_usage'] = child.usage
                        need(not result['retirement']['remaining'], 'Smoke descendants retired')
                    except Exception as error:
                        result['complete'] = False
                        result['errors'].append({'retirement_error': str(error)})
                if control is not None:
                    try:
                        control.close()
                    except Exception as error:
                        result['complete'] = False
                        result['errors'].append({'control_close_error': str(error)})
        verify_state(self.source, self.expected)
        same_bytes(expected_path, result['expected_results'])
        spec = save(stage / 'smoke.json', result)
        self.stages.append({'label': 'smoke', 'receipt': spec, 'complete': result['complete']})
        need(result['complete'], 'Smoke failed; raw responses and logs retained')

    def execute(self):
        if self.args.smoke:
            self.smoke()
            return
        baseline = None
        if self.args.variant != 'P':
            need(self.pristine is not None, 'Non-P complete qualification requires --pristine-binary')
            baseline = self.unit('qualify-P', self.pristine, 'P', 'qualification')['qualification']
        qualified = self.unit('qualify-' + self.args.variant, self.target, self.args.variant,
                              'qualification', baseline=baseline)['qualification']
        if self.args.mode != 'qualify':
            self.unit('paid-' + self.args.variant, self.target, self.args.variant, 'paid',
                      qualification=qualified, mode=self.args.mode)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', type=Path, help='Artifact repository root (inferred from this script)')
    parser.add_argument('--binary', type=Path, required=True, help='Built benchmark-rpc for the requested variant')
    parser.add_argument('--pristine-binary', type=Path, help='Pristine benchmark-rpc; required for complete non-P qualification')
    parser.add_argument('--state-dir', type=Path, required=True, help='Unpacked closed state directory or archive root')
    parser.add_argument('--cohort', required=True, choices=('E', 'A'))
    parser.add_argument('--variant', required=True, choices=('P', 'S', 'C', 'V'))
    parser.add_argument('--output', required=True, type=Path, help='Fresh workspace, never reused')
    parser.add_argument('--mode', choices=('qualify', 'serial', 'concurrent', 'replay'), default='qualify')
    parser.add_argument('--source-variant', choices=('pristine', 'accepted-service', 'current-service'))
    parser.add_argument('--smoke', action='store_true', help='Small functional subset; qualify mode only, never paid-eligible')
    parser.add_argument('--server-cpus', help='One CPU for serial/qualify; four comma-separated CPUs for concurrent/replay')
    parser.add_argument('--client-cpus', help='Same CPU count as server mask; disjoint physical cores on same socket/NUMA node')
    parser.add_argument('--http-port', type=int, default=0, help='Loopback port; zero chooses an available unprivileged port')
    parser.add_argument('--auth-port', type=int, default=0)
    parser.add_argument('--timeout', type=int, default=1800, help='Per-unit deadline; concurrent/replay retain exactly 1800 seconds')
    parser.add_argument('--lock-fd', type=int, help='Optional already-held outer lock FD to inherit into children')
    args = parser.parse_args()
    if sys.platform != 'linux' or not hasattr(os, 'wait4') or not shutil.which('taskset'):
        parser.error('Linux, taskset, /proc, CPU topology and wait4 are required')
    need(1 <= args.timeout <= 43200, 'Bounded timeout required')
    need(not args.smoke or args.mode == 'qualify', '--smoke is only a functional qualification subset')
    need(args.mode not in ('serial', 'concurrent') or args.variant in ('P', 'S', 'C'), 'Main service uses P/S/C')
    need(args.mode != 'replay' or args.variant in ('P', 'V', 'C'), 'Replay service uses P/V/C')
    need(args.mode not in ('concurrent', 'replay') or args.timeout == 1800, 'Concurrent/replay timeout remains 1800 seconds')
    if args.variant == 'P':
        need(args.source_variant in (None, 'pristine'), 'P requires a pristine build')
        args.source_variant = 'pristine'
    elif args.mode in ('serial', 'concurrent'):
        need(args.source_variant == 'accepted-service', 'Historical serial/main results require explicit --source-variant accepted-service')
    elif args.mode == 'replay':
        need(args.source_variant == 'current-service', 'Replay requires explicit --source-variant current-service')
    else:
        args.source_variant = args.source_variant or 'current-service'
        need(args.source_variant != 'pristine', 'S/C/V require an accepted-service or current-service build')
    need(args.variant != 'V' or args.source_variant == 'current-service', 'V requires current-service')
    need(args.smoke or args.variant == 'P' or args.pristine_binary, 'Complete non-P qualification requires --pristine-binary')
    if args.lock_fd is not None:
        os.fstat(args.lock_fd)
    def interrupted(signum, _frame):
        raise KeyboardInterrupt('Owned RPC run interrupted by signal ' + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    run = None
    try:
        run = Run.__new__(Run)
        run.__init__(args)
        run.execute()
        result = {'schema': 'gas-certificate-rpc-run-v1', 'complete': True, 'mode': args.mode,
            'cohort': args.cohort, 'variant': args.variant, 'subset_only': args.smoke,
            'recorded_paper_result': False, 'stages': run.stages}
        save(run.root / 'run.json', result)
        print(json.dumps(result, sort_keys=True), flush=True)
    except (Exception, KeyboardInterrupt) as error:
        result = {'schema': 'gas-certificate-rpc-run-v1', 'complete': False,
            'error': str(error), 'traceback': traceback.format_exc(), 'stages': getattr(run, 'stages', [])}
        if run is not None and getattr(run, 'created_output', False):
            save(run.root / 'run.json', result)
        print(json.dumps(result, sort_keys=True), file=sys.stderr, flush=True)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
