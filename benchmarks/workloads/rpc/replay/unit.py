"""Run one server with four concurrent clients using caller-prepared manifests.

The caller prepares binaries, database copies, and the experiment schedule and
supplies the lifecycle module. Importing this module defines helpers. This module
exclusively reaps its five direct children with wait4.
"""
import importlib.util
import json
import os
import selectors
import subprocess
import sys
import time
import traceback
from pathlib import Path

_definition = importlib.util.spec_from_file_location('round1810_client_support', Path(__file__).with_name('client.py'))
support = importlib.util.module_from_spec(_definition)
_definition.loader.exec_module(support)
need, path, identity, verify, read, load, write = support.need, support.path, support.identity, support.verify, support.read, support.load, support.write


def birth(pid):
    try:
        return Path('/proc/' + str(pid) + '/stat').read_text().rsplit(')', 1)[1].split()[19]
    except FileNotFoundError:
        return None


class Wait4Child:
    """Reap through this adapter's poll/wait methods to preserve wait4 rusage."""
    def __init__(self, process, role, launch_monotonic_ns):
        self.role = role
        self.launch_monotonic_ns = launch_monotonic_ns
        self.process = process
        self.pid = process.pid
        self.birth = birth(self.pid)
        self.returncode = None
        self.usage = None

    def poll(self):
        if self.returncode is None:
            # ChildProcessError is fatal evidence of a competing reaper.
            pid, status, usage = os.wait4(self.pid, os.WNOHANG)
            if pid:
                need(pid == self.pid, 'wait4 exact direct child')
                self.returncode = os.waitstatus_to_exitcode(status)
                self.process.returncode = self.returncode
                self.usage = {
                    'role': self.role, 'launch_monotonic_ns': self.launch_monotonic_ns, 'reaped_monotonic_ns': time.monotonic_ns(),
                    'pid': pid, 'wait_status': status, 'returncode': self.returncode,
                    'user_seconds': usage.ru_utime, 'system_seconds': usage.ru_stime,
                    'user_ns': round(usage.ru_utime * 1000000000),
                    'system_ns': round(usage.ru_stime * 1000000000),
                    'whole_process_cpu_ns': round((usage.ru_utime + usage.ru_stime) * 1000000000),
                    'max_rss_linux_kib': usage.ru_maxrss, 'minor_faults': usage.ru_minflt,
                    'major_faults': usage.ru_majflt, 'voluntary_context_switches': usage.ru_nvcsw,
                    'involuntary_context_switches': usage.ru_nivcsw,
                    'boundary': 'exact direct child taskset exec through actual exit; server includes startup/indexing/warmup/service/GC/closure, client includes connection/preencoding/body/deferred validation/output; client CPU is not body saturation evidence',
                    'exclusive_wait4_owner': True,
                }
        return self.returncode

    def wait(self, timeout=None):
        deadline = None if timeout is None else time.monotonic() + timeout
        while self.poll() is None:
            if deadline is not None and time.monotonic() >= deadline:
                raise subprocess.TimeoutExpired('exact child wait4', timeout)
            time.sleep(.02)
        return self.returncode


class Control:
    def __init__(self, process, owner, journal):
        self.process, self.owner, self.journal = process, owner, journal
        self.buffer = b''
        self.selector = selectors.DefaultSelector()
        self.selector.register(process.stdout, selectors.EVENT_READ)

    def send(self, value):
        self.process.stdin.write(json.dumps({'command': value}).encode() + b'\n')
        self.process.stdin.flush()

    def receive(self, expected, deadline):
        while b'\n' not in self.buffer:
            need(time.monotonic() < deadline, 'bounded whole-unit control wait: ' + expected)
            events = self.selector.select(min(.2, max(0, deadline - time.monotonic())))
            if events:
                part = os.read(self.process.stdout.fileno(), 1 << 20)
                need(part, 'control EOF before ' + expected)
                self.buffer += part
                need(len(self.buffer) <= 8 << 20, 'bounded control buffer')
            elif self.owner.poll() is not None:
                # A final event may already be readable even though the child exited.
                events = self.selector.select(0)
                need(events, 'child exited before ' + expected)
        raw, self.buffer = self.buffer.split(b'\n', 1)
        self.journal.write(raw + b'\n')
        self.journal.flush()
        event = json.loads(raw)
        need(event.get('kind') == expected, 'expected ' + expected + ', received ' + str(event))
        event['controller_received_monotonic_ns'] = time.monotonic_ns()
        return event

    def close(self):
        self.selector.close()
        for pipe in (self.process.stdin, self.process.stdout):
            pipe.close()


SERVER_CPUS = support.SERVER_CPUS
CLIENT_CPUS = support.CLIENT_CPUS
WORKERS = support.WORKERS


def cpulist(value):
    result = set()
    for part in value.strip().split(','):
        bounds = part.split('-')
        need(1 <= len(bounds) <= 2, 'ordinary kernel CPU list')
        low, high = int(bounds[0]), int(bounds[-1])
        need(0 <= low <= high, 'bounded CPU range')
        result.update(range(low, high + 1))
    return result


def topology():
    selected = set(SERVER_CPUS + CLIENT_CPUS)
    allowed = set(os.sched_getaffinity(0))
    need(len(selected) == 8 and selected <= allowed, 'frozen eight cores must be in actual allowed affinity; no replacement')
    rows, physical = [], set()
    for cpu in sorted(selected):
        directory = Path('/sys/devices/system/cpu/cpu' + str(cpu))
        topo = directory / 'topology'
        package = int((topo / 'physical_package_id').read_text())
        core = int((topo / 'core_id').read_text())
        die_path = topo / 'die_id'
        die = int(die_path.read_text()) if die_path.exists() else None
        siblings = cpulist((topo / 'thread_siblings_list').read_text())
        need(cpu in siblings and not ((siblings - {cpu}) & selected), 'no selected SMT siblings')
        key = (package, die, core)
        need(key not in physical, 'selected distinct physical cores')
        physical.add(key)
        nodes = sorted(int(p.name[4:]) for p in directory.glob('node[0-9]*'))
        rows.append({'cpu': cpu, 'socket_id': package, 'die_id': die, 'core_id': core, 'thread_siblings': sorted(siblings), 'numa_nodes': nodes})
    return {'allowed_cpus': sorted(allowed), 'server_cpus': list(SERVER_CPUS), 'client_cpus': list(CLIENT_CPUS), 'selected_topology': rows, 'distinct_physical_cores': True, 'selected_SMT_siblings': False}


def affinity(owner, expected):
    leader = sorted(os.sched_getaffinity(owner.pid))
    need(leader == list(expected), 'actual child leader affinity matches frozen CPU mask')
    tasks = {}
    for task in sorted((Path('/proc') / str(owner.pid) / 'task').iterdir()):
        try:
            mask = sorted(os.sched_getaffinity(int(task.name)))
        except ProcessLookupError:
            continue
        need(mask == list(expected), 'actual existing child thread affinity matches mask')
        tasks[task.name] = mask
    need(str(owner.pid) in tasks, 'actual live direct child affinity snapshot')
    return {'leader': leader, 'tasks': tasks, 'observed_monotonic_ns': time.monotonic_ns()}


def latency_union(client_receipts, count, warm, measured):
    # Analyze latency after END, STOP, and server wait4, outside the server CPU boundary.
    all_values = []
    for client_id, receipt in enumerate(client_receipts):
        verify(receipt['response_stream'])
        values = []
        seen = 0
        with path(receipt['response_stream']['path']).open('rb') as stream:
            for line in stream:
                need(len(line) <= 24 << 20, 'bounded serialized complete response row')
                row = json.loads(line)
                label = 'warmup' if seen < count * warm else 'measured'
                offset = seen if label == 'warmup' else seen - count * warm
                need(row['kind'] == 'estimate' and row['client_id'] == client_id and (row['phase'], row['pass'], row['position']) == (label, offset // count, offset % count), 'whole raw client/pass/position key')
                need(row['http_elapsed_ns'] == row['http_end_monotonic_ns'] - row['http_start_monotonic_ns'] >= 0, 'raw actual HTTP duration')
                if label == 'measured':
                    values.append(row['http_elapsed_ns'])
                seen += 1
        need(seen == count * (warm + measured) and len(values) == count * measured, 'all raw warmup and measured rows')
        need(support.distribution(values) == receipt['measured_http_latency'], 'client complete HTTP distribution matches whole raw stream')
        all_values.extend(values)
    need(len(all_values) == WORKERS * count * measured, 'complete four-client latency union')
    return support.distribution(all_values)


def run_unit(server_manifest_path, client_manifest_paths, output_directory, lifecycle_module,
             timeout_seconds=1800, taskset='/usr/bin/taskset', inherited_lock_fd=None, environment=None):
    """Run one fixed four-client paid unit with qualified manifests.

    The caller prepares builds, state copies, and the experiment schedule, and
    supplies qualified server inputs, four client manifests, and lifecycle support.
    RPC traffic follows these manifests. Each direct child is reaped exclusively
    through its Wait4Child adapter to preserve resource usage.
    """
    need(sys.platform == 'linux' and hasattr(os, 'wait4'), 'Linux exact-child wait4')
    need(timeout_seconds == 1800, 'frozen whole-unit timeout')
    need(isinstance(client_manifest_paths, (list, tuple)) and len(client_manifest_paths) == WORKERS, 'exactly four client manifests in client_id order')
    sm_path = str(path(server_manifest_path))
    sm = read(sm_path)
    cm_paths = [str(path(p)) for p in client_manifest_paths]
    need(len(set(cm_paths)) == WORKERS, 'distinct client manifests')
    cms = [read(p) for p in cm_paths]
    need(sm.get('schema') == 'round1789-server-input-v1' and sm['purpose'] == 'paid' and sm['variant'] in ('P', 'V', 'C') and sm['cohort'] in support.COUNTS, 'unchanged root-released qualified paid server')
    sm_spec = identity(sm_path)
    count, rejects, warm, measured = support.COUNTS[sm['cohort']]
    for client_id, cm in enumerate(cms):
        need(cm.get('schema') == 'round1810-client-input-v1' and cm['server_manifest'] == sm_spec, 'exact root-released concurrent client manifest')
        need(type(cm['client_id']) is int and cm['client_id'] == client_id and cm['worker_count'] == WORKERS and cm['client_cpu'] == CLIENT_CPUS[client_id], 'fixed client identity/CPU')
        need((cm['warmup_passes'], cm['measured_passes']) == (warm, measured) and cm['raw_response_cap_bytes'] == 16 << 20 and cm['response_body_cap'] == 8 << 20 and cm['http_timeout_seconds'] == 120 and cm['baseline_qualification'] is None, 'frozen exact per-client load and caps')
    need(Path(taskset).is_absolute() and Path(taskset).is_file(), 'explicit installed taskset')
    verify(sm['server_binary'])
    protected = [sm_spec, *[identity(p) for p in cm_paths], identity(str(Path(__file__).resolve())), identity(str(Path(__file__).with_name('client.py').resolve()))]
    out = path(output_directory, exists=False)
    directories = [out, path(sm['output_directory'], exists=False), *[path(cm['output_directory'], exists=False) for cm in cms]]
    need(len(set(directories)) == WORKERS + 2 and all(not a.is_relative_to(b) for a in directories for b in directories if a != b), 'fresh distinct nonnested unit/server/client outputs')
    out.mkdir(mode=0o700)
    report = {'schema': 'round1810-concurrent-unit-v1', 'complete': False, 'purpose': 'paid', 'cohort': sm['cohort'], 'variant': sm['variant'], 'server_binary': sm['server_binary'], 'context': sm['context'], 'population': sm['population'], 'worker_count': WORKERS, 'errors': [], 'performance_measured': True, 'forced_gc_invocations': 0, 'exclusive_server_wait4_owner': True, 'exclusive_wait4_owner_all_children': True, 'protected_inputs': protected, 'clients': {str(i): {'manifest': identity(cm_paths[i]), 'client_id': i, 'requested_cpu': CLIENT_CPUS[i]} for i in range(WORKERS)}, 'clock': 'host time.monotonic_ns', 'functional_verification_extra_rpc_count': 0, 'combined_raw_response_cap_bytes': 64 << 20, 'requested_server_gomaxprocs': 4, 'requested_server_cpus': list(SERVER_CPUS)}
    deadline = time.monotonic() + timeout_seconds
    server = server_ctl = None
    clients, controls, handles = [], [], []
    success = False
    base_env = os.environ.copy()
    if environment is not None:
        need(isinstance(environment, dict) and all(isinstance(k, str) and isinstance(v, str) for k, v in environment.items()), 'explicit child-only environment overrides')
        base_env.update(environment)
    env = base_env.copy()
    for key in ('GOGC', 'GOMEMLIMIT', 'GODEBUG'):
        env.pop(key, None)
    env['GOMAXPROCS'] = '4'
    report['gc_environment'] = {'GOGC': None, 'GOMEMLIMIT': None, 'GODEBUG': None, 'requested_GOMAXPROCS': '4'}
    report['child_only_environment_override_names'] = sorted(environment or {})
    passed = () if inherited_lock_fd is None else (inherited_lock_fd,)
    try:
        report['topology'] = topology()  # Validate the configured CPU topology before launching any child.
        def opened(name):
            f = (out / name).open('xb')
            handles.append(f)
            return f

        launched = time.monotonic_ns()
        process = subprocess.Popen([taskset, '-c', ','.join(map(str, SERVER_CPUS)), sm['server_binary']['path'], '--manifest', sm_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=opened('server.stderr.log'), env=env, start_new_session=True, pass_fds=passed, bufsize=0)
        server = Wait4Child(process, 'server', launched)
        server_ctl = Control(process, server, opened('server.control.jsonl'))
        report['server_child'] = {'pid': server.pid, 'birth': server.birth, 'direct_child': True}
        ready = server_ctl.receive('READY', deadline)
        need(ready['pid'] == server.pid and all(ready[k] == sm[k] for k in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population')), 'exact READY server child and manifest')
        report['ready'] = ready
        report['server_affinity_ready'] = affinity(server, SERVER_CPUS)
        for client_id in range(WORKERS):
            launched = time.monotonic_ns()
            process = subprocess.Popen([taskset, '-c', str(CLIENT_CPUS[client_id]), sys.executable, '-I', '-X', 'utf8', '-B', str(Path(__file__).with_name('client.py').resolve()), '--manifest', cm_paths[client_id]], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=opened('client-' + str(client_id) + '.stderr.log'), env=base_env, start_new_session=True, pass_fds=passed, bufsize=0)
            owner = Wait4Child(process, 'client-' + str(client_id), launched)
            clients.append(owner)
            controls.append(Control(process, owner, opened('client-' + str(client_id) + '.control.jsonl')))
            report['clients'][str(client_id)]['child'] = {'pid': owner.pid, 'birth': owner.birth, 'direct_child': True}
        for client_id, ctl in enumerate(controls):
            ready = ctl.receive('READY', deadline)
            need(ready['pid'] == clients[client_id].pid and ready['client_id'] == client_id and ready['server_manifest'] == sm_spec and ready['observed_affinity'] == [CLIENT_CPUS[client_id]] and all(ready[k] == sm[k] for k in ('purpose', 'cohort', 'variant')), 'exact independent connected client READY')
            report['clients'][str(client_id)]['ready'] = ready
            report['clients'][str(client_id)]['affinity_ready'] = affinity(clients[client_id], (CLIENT_CPUS[client_id],))
        def broadcast(command):
            sent = []
            for client_id, ctl in enumerate(controls):
                before = time.monotonic_ns()
                ctl.send(command)
                sent.append({'client_id': client_id, 'before_monotonic_ns': before, 'after_monotonic_ns': time.monotonic_ns()})
            return sent
        report['warmup_broadcast'] = broadcast('WARMUP')
        for client_id, ctl in enumerate(controls):
            event = ctl.receive('WARMUP_DONE', deadline)
            need(event['client_id'] == client_id and event['response_count'] == event['timing']['response_count'] == count * warm, 'every client complete warmup before BEGIN')
            report['clients'][str(client_id)]['warmup_done'] = event
        server_ctl.send('BEGIN')
        report['begin'] = server_ctl.receive('BEGIN', deadline)
        report['common_release_monotonic_ns'] = time.monotonic_ns()
        report['measure_broadcast'] = broadcast('MEASURE')  # all four sends precede all receives
        for client_id, ctl in enumerate(controls):
            event = ctl.receive('MEASURED', deadline)
            need(event['client_id'] == client_id and event['response_count'] == count * (warm + measured) and event['timing']['response_count'] == count * measured, 'every client full measured body drained')
            timing = event['timing']
            need(report['common_release_monotonic_ns'] <= timing['start_monotonic_ns'] <= timing['first_http_start_monotonic_ns'] <= timing['last_http_end_monotonic_ns'] <= timing['end_monotonic_ns'] <= event['controller_received_monotonic_ns'], 'actual common release/worker/HTTP/drain enclosure')
            report['clients'][str(client_id)]['measured_done'] = event
        report['all_measured_received_monotonic_ns'] = time.monotonic_ns()
        server_ctl.send('END')
        report['end'] = server_ctl.receive('END', deadline)
        report['server_affinity_after_end'] = affinity(server, SERVER_CPUS)
        report['finalize_broadcast'] = broadcast('FINALIZE')
        for client_id, ctl in enumerate(controls):
            event = ctl.receive('FINALIZED', deadline)
            need(event['client_id'] == client_id and event['response_count'] == count * (warm + measured), 'every client deferred complete response validation/write')
            report['clients'][str(client_id)]['finalized'] = event
        for owner in clients:
            need(owner.wait(timeout=max(.01, deadline - time.monotonic())) == 0 and owner.usage is not None, 'actual independent client exit0 with exact wait4 rusage')
        server_ctl.send('STOP')
        report['closed'] = server_ctl.receive('CLOSED', deadline)
        need(report['closed']['context_unchanged'] is True and server.wait(timeout=max(.01, deadline - time.monotonic())) == 0 and server.usage is not None, 'server STOP/context/closure/exit0/exact wait4')
        sr_path = str(path(sm['output_directory']) / 'receipt.json')
        sr = read(sr_path)
        need(sr.get('schema') == 'round1789-server-receipt-v1' and sr.get('complete') and sr.get('stack_closed') and sr.get('inputs_preserved') and not sr['errors'], 'whole unchanged server receipt')
        need(sr['context_before'] == sr['context_after_readiness'] == sr['context_after'] and sr['history_opening']['complete'] and sr['storage_opening_policy']['no_pruning'] and sr['forced_gc_invocations'] == 0 and sr['requests_issued_by_server'] == 0, 'identical archive opening/full contexts and no extra request driver')
        for key in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population'):
            need(sr[key] == sm[key], 'server receipt manifest binding')
        for spec in sr['protected_inputs']:
            verify(spec)
        client_receipts = []
        for client_id, cm in enumerate(cms):
            cr_path = str(path(cm['output_directory']) / 'receipt.json')
            cr = read(cr_path)
            need(cr.get('schema') == 'round1810-client-receipt-v1' and cr.get('complete') and cr.get('inputs_preserved') and not cr['errors'], 'whole client receipt')
            need(cr['client_id'] == client_id and cr['worker_count'] == WORKERS and cr['observed_affinity'] == [CLIENT_CPUS[client_id]], 'exact client role/observed affinity')
            need(cr['response_count'] == cr['requests_issued'] == count * (warm + measured) and cr['warmup_response_count'] == count * warm and cr['measured_response_count'] == count * measured and cr['functional_verification_extra_rpc_count'] == 0 and cr['all_responses_validated_after_end'], 'exact complete per-client work and no extra RPC')
            need(cr['expected_reverts_per_pass'] == rejects and cr['total_raw_response_cap_bytes'] == 16 << 20 and cr['buffered_raw_response_bytes'] <= 16 << 20, 'all original rejects and bounded client buffer')
            need(cr['phase_timings']['measured'] == report['clients'][str(client_id)]['measured_done']['timing'] and cr['phase_timings']['warmup'] == report['clients'][str(client_id)]['warmup_done']['timing'], 'actual client phase timing matches control evidence')
            for key in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population'):
                need(cr[key] == sm[key], 'client receipt manifest binding')
            for spec in cr['protected_inputs']:
                verify(spec)
            verify(cr['response_stream'])
            report['clients'][str(client_id)].update(receipt=identity(cr_path), response_stream=cr['response_stream'], measured_http_latency=cr['measured_http_latency'], phase_timings=cr['phase_timings'], buffered_raw_response_bytes=cr['buffered_raw_response_bytes'])
            client_receipts.append(cr)
        report['buffered_raw_response_bytes'] = sum(cr['buffered_raw_response_bytes'] for cr in client_receipts)
        need(report['buffered_raw_response_bytes'] <= 64 << 20, 'fixed combined four-client raw buffer cap')
        report['server_receipt'] = identity(sr_path)
        report['response_streams'] = [cr['response_stream'] for cr in client_receipts]
        report['response_count'] = WORKERS * count * (warm + measured)
        report['warmup_response_count'] = WORKERS * count * warm
        report['measured_response_count'] = WORKERS * count * measured
        report['measured_http_latency'] = latency_union(client_receipts, count, warm, measured)
        release, drained = report['common_release_monotonic_ns'], report['all_measured_received_monotonic_ns']
        need(drained > release, 'positive common controller release-to-all-MEASURED wall time')
        report['measured_batch_wall_ns'] = drained - release
        report['closed_loop_throughput'] = {'measured_responses': WORKERS * count * measured, 'wall_ns': drained - release, 'responses_per_second': WORKERS * count * measured * 1e9 / (drained - release), 'boundary': 'common controller release immediately before all MEASURE sends through receipt of all four MEASURED events; includes command/release/drain skew'}
        timings = [cr['phase_timings']['measured'] for cr in client_receipts]
        report['client_timing_envelope'] = {'first_start_monotonic_ns': min(t['start_monotonic_ns'] for t in timings), 'last_end_monotonic_ns': max(t['end_monotonic_ns'] for t in timings), 'first_http_monotonic_ns': min(t['first_http_start_monotonic_ns'] for t in timings), 'last_http_monotonic_ns': max(t['last_http_end_monotonic_ns'] for t in timings), 'start_skew_ns': max(t['start_monotonic_ns'] for t in timings) - min(t['start_monotonic_ns'] for t in timings), 'end_skew_ns': max(t['end_monotonic_ns'] for t in timings) - min(t['end_monotonic_ns'] for t in timings)}
        begin, end = sr['begin'], sr['end']
        user = end['cpu_before']['user_ns'] - begin['cpu_after']['user_ns']
        system = end['cpu_before']['system_ns'] - begin['cpu_after']['system_ns']
        wall = end['monotonic_before_ns'] - begin['monotonic_after_ns']
        need(user >= 0 and system >= 0 and wall > 0, 'actual fenced process CPU/wall intervals')
        report['fenced_server_cpu'] = {'user_ns': user, 'system_ns': system, 'total_ns': user + system, 'wall_ns': wall, 'cpu_per_wall': (user + system) / wall, 'boundary': 'post-BEGIN snapshot to pre-END snapshot; all raw fence costs retained without subtraction'}
        report['normal_gc_collections_between_fences'] = end['normal_gc_heap']['num_gc'] - begin['normal_gc_heap']['num_gc']
        report['gc_amortization_resolved'] = report['normal_gc_collections_between_fences'] >= 2
        report['normal_gc_heap_begin'], report['normal_gc_heap_end'] = begin['normal_gc_heap'], end['normal_gc_heap']
        for spec in protected:
            verify(spec)
        success = True
    except Exception as error:
        report['errors'].append({'error': str(error), 'traceback': traceback.format_exc()})
    finally:
        # Each exact child has a single wait4 adapter, including all cleanup paths.
        for label, owner in [*[(str(i), child) for i, child in enumerate(clients)], ('server', server)]:
            if owner is None:
                continue
            target = report if label == 'server' else report['clients'][label]
            try:
                retired = lifecycle_module.retire(owner)
                target['server_retirement' if label == 'server' else 'retirement'] = retired
                need(retired['returncode'] is not None and not retired['remaining'], 'fully retired direct child ' + label)
            except Exception as error:
                report['errors'].append({'error': label + ' retirement: ' + str(error), 'traceback': traceback.format_exc()})
            target['whole_process_server_rusage' if label == 'server' else 'whole_process_rusage'] = owner.usage
            if owner.usage is None:
                report['errors'].append({'error': label + ' missing exclusive wait4 rusage'})
        for client_id, owner in enumerate(clients):
            target = report['clients'][str(client_id)]
            directory = Path(cms[client_id]['output_directory'])
            try:
                completed = directory / 'receipt.json'
                failed = directory / 'failure.json'
                if completed.is_file() or failed.is_file():
                    artifact = completed if completed.is_file() else failed
                    retained = read(str(artifact));verify(retained['response_stream'])
                    target['retained_response_evidence'] = {'receipt': identity(str(artifact)), 'response_stream': retained['response_stream'], 'complete': retained['complete']}
                else:
                    target['retained_response_evidence'] = None
                    report['errors'].append({'error': 'client ' + str(client_id) + ' has no completed response-prefix artifact; never an accepted partial unit'})
            except Exception as error:
                report['errors'].append({'error': 'client prefix preservation: ' + str(error)})
        for ctl in [*controls, server_ctl]:
            if ctl is not None:
                try:
                    ctl.close()
                except Exception as error:
                    report['errors'].append({'error': 'control close: ' + str(error)})
        for handle in handles:
            handle.close()
        try:
            for spec in protected:
                verify(spec)
        except Exception as error:
            report['errors'].append({'error': 'protected helper inputs: ' + str(error)})
    report['children_retired'] = all(owner.returncode is not None for owner in clients) and (server is None or server.returncode is not None) and all(not report['clients'][str(i)].get('retirement', {'remaining': [True]})['remaining'] for i in range(len(clients))) and (server is None or not report.get('server_retirement', {'remaining': [True]})['remaining'])
    report['complete'] = success and len(clients) == WORKERS and report['children_retired'] and not report['errors']
    report['receipt'] = str(out / 'receipt.json')
    write(report['receipt'], report)
    return report
