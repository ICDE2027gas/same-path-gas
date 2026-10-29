"""Run one server/client unit using caller-prepared manifests.

The caller prepares binaries, database copies, and the experiment schedule and
supplies the lifecycle module. Importing this module defines helpers. This module
exclusively reaps its direct server child with wait4.
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

_definition = importlib.util.spec_from_file_location('round1789_client_support', Path(__file__).with_name('client.py'))
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
    def __init__(self, process):
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
                need(pid == self.pid, 'wait4 exact direct server child')
                self.returncode = os.waitstatus_to_exitcode(status)
                self.process.returncode = self.returncode
                self.usage = {
                    'pid': pid, 'wait_status': status, 'returncode': self.returncode,
                    'user_seconds': usage.ru_utime, 'system_seconds': usage.ru_stime,
                    'user_ns': round(usage.ru_utime * 1000000000),
                    'system_ns': round(usage.ru_stime * 1000000000),
                    'whole_process_cpu_ns': round((usage.ru_utime + usage.ru_stime) * 1000000000),
                    'max_rss_linux_kib': usage.ru_maxrss, 'minor_faults': usage.ru_minflt,
                    'major_faults': usage.ru_majflt, 'voluntary_context_switches': usage.ru_nvcsw,
                    'involuntary_context_switches': usage.ru_nivcsw,
                    'boundary': 'exact direct child from taskset exec through actual server exit; includes startup, warmup, all HTTP service, context/input checks, normal GC, shutdown and reporting',
                    'exclusive_wait4_owner': True,
                }
        return self.returncode

    def wait(self, timeout=None):
        deadline = None if timeout is None else time.monotonic() + timeout
        while self.poll() is None:
            if deadline is not None and time.monotonic() >= deadline:
                raise subprocess.TimeoutExpired('exact server wait4', timeout)
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
        return event

    def close(self):
        self.selector.close()
        for pipe in (self.process.stdin, self.process.stdout):
            pipe.close()


def run_unit(server_manifest_path, client_manifest_path, output_directory, lifecycle_module,
             timeout_seconds=1800, taskset='/usr/bin/taskset', inherited_lock_fd=None, environment=None):
    """Run one unit and reap its children before returning.

    Qualification: one complete population plus positive-budget eth_call checks.
    Paid: fixed warmups, BEGIN, fixed measured passes, END, validate/write, STOP.
    The caller owns source/DB verification, builds, locks, and the cohort schedule.
    This function retains exclusive responsibility for reaping its children.
    """
    need(sys.platform == 'linux' and hasattr(os, 'wait4'), 'Linux exact-child wait4')
    need(1 <= timeout_seconds <= 43200, 'bounded whole-unit timeout')
    sm_path, cm_path = str(path(server_manifest_path)), str(path(client_manifest_path))
    sm, cm = read(sm_path), read(cm_path)
    need(sm.get('schema') == 'round1789-server-input-v1', 'root-released server manifest')
    need(cm.get('schema') == 'round1789-client-input-v1' and cm['server_manifest'] == identity(sm_path), 'exact paired released client manifest')
    need(sm['purpose'] in ('qualification', 'paid') and sm['variant'] in ('P', 'S', 'C', 'V') and sm['cohort'] in support.COUNTS, 'purpose/variant/cohort')
    need(Path(taskset).is_absolute() and Path(taskset).is_file(), 'explicit installed taskset')
    verify(sm['server_binary'])
    protected = [identity(sm_path), identity(cm_path), identity(str(Path(__file__).resolve())), identity(str(Path(__file__).with_name('client.py').resolve()))]
    out = path(output_directory, exists=False)
    out.mkdir(mode=0o700)
    need(out != path(sm['output_directory'], exists=False) and out != path(cm['output_directory'], exists=False), 'distinct unit/server/client outputs')
    report = {'schema': 'round1789-unit-v1', 'complete': False, 'purpose': sm['purpose'], 'cohort': sm['cohort'], 'variant': sm['variant'], 'server_binary': sm['server_binary'], 'context': sm['context'], 'population': sm['population'], 'qualification': None, 'errors': [], 'performance_measured': sm['purpose'] == 'paid', 'forced_gc_invocations': 0, 'exclusive_server_wait4_owner': True, 'protected_inputs': protected}
    deadline = time.monotonic() + timeout_seconds
    server = client = server_ctl = client_ctl = None
    handles = []
    success = False
    server_receipt = client_receipt = None
    base_env = os.environ.copy()
    if environment is not None:
        need(isinstance(environment, dict) and all(isinstance(k, str) and isinstance(v, str) for k, v in environment.items()), 'explicit child-only environment overrides')
        base_env.update(environment)
    env = base_env.copy()
    env.pop('GOGC', None)
    env.pop('GOMEMLIMIT', None)
    env.pop('GODEBUG', None)
    env['GOMAXPROCS'] = '1'
    report['gc_environment'] = {'GOGC': None, 'GOMEMLIMIT': None, 'GODEBUG': None, 'GOMAXPROCS': '1'}
    report['child_only_environment_override_names'] = sorted(environment or {})
    passed = () if inherited_lock_fd is None else (inherited_lock_fd,)
    try:
        def opened(name):
            f = (out / name).open('xb')
            handles.append(f)
            return f

        process = subprocess.Popen([taskset, '-c', os.environ.get('GAS_CERTIFICATE_SERIAL_SERVER_CPU', '0'), sm['server_binary']['path'], '--manifest', sm_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=opened('server.stderr.log'), env=env, start_new_session=True, pass_fds=passed, bufsize=0)
        server = Wait4Child(process)  # Assign exclusive wait4 ownership immediately after Popen.
        server_ctl = Control(process, server, opened('server.control.jsonl'))
        report['server_child'] = {'pid': server.pid, 'birth': server.birth, 'direct_child': True, 'taskset_cpu': int(os.environ.get('GAS_CERTIFICATE_SERIAL_SERVER_CPU', '0'))}
        ready = server_ctl.receive('READY', deadline)
        need(ready['pid'] == server.pid and all(ready[k] == sm[k] for k in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population')), 'exact READY server child and manifest')
        report['ready'] = ready
        client = subprocess.Popen([taskset, '-c', os.environ.get('GAS_CERTIFICATE_SERIAL_CLIENT_CPU', '1'), sys.executable, '-I', '-X', 'utf8', '-B', str(Path(__file__).with_name('client.py').resolve()), '--manifest', cm_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=opened('client.stderr.log'), env=base_env, start_new_session=True, pass_fds=passed, bufsize=0)
        client.birth = birth(client.pid)
        client_ctl = Control(client, client, opened('client.control.jsonl'))
        client_ready = client_ctl.receive('READY', deadline)
        need(client_ready['pid'] == client.pid and client_ready['server_manifest'] == identity(sm_path) and all(client_ready[k] == sm[k] for k in ('purpose', 'cohort', 'variant')), 'exact separate HTTP client child')
        report['client_child'] = {'pid': client.pid, 'birth': client.birth, 'taskset_cpu': int(os.environ.get('GAS_CERTIFICATE_SERIAL_CLIENT_CPU', '1'))}
        if sm['purpose'] == 'qualification':
            client_ctl.send('QUALIFY')
            report['client_done'] = client_ctl.receive('QUALIFIED', deadline)
        else:
            client_ctl.send('WARMUP')
            report['warmup_done'] = client_ctl.receive('WARMUP_DONE', deadline)
            server_ctl.send('BEGIN')
            report['begin'] = server_ctl.receive('BEGIN', deadline)
            client_ctl.send('MEASURE')
            report['measured_done'] = client_ctl.receive('MEASURED', deadline)
            server_ctl.send('END')
            report['end'] = server_ctl.receive('END', deadline)
            client_ctl.send('FINALIZE')
            report['client_done'] = client_ctl.receive('FINALIZED', deadline)
        need(client.wait(timeout=max(.01, deadline - time.monotonic())) == 0, 'whole HTTP client normal exit')
        server_ctl.send('STOP')
        report['closed'] = server_ctl.receive('CLOSED', deadline)
        need(report['closed']['context_unchanged'] is True, 'server closed with unchanged context')
        need(server.wait(timeout=max(.01, deadline - time.monotonic())) == 0 and server.usage is not None, 'whole server normal exit with exact wait4 rusage')
        server_receipt_path = str(path(sm['output_directory']) / 'receipt.json')
        client_receipt_path = str(path(cm['output_directory']) / 'receipt.json')
        server_receipt, client_receipt = read(server_receipt_path), read(client_receipt_path)
        need(server_receipt.get('schema') == 'round1789-server-receipt-v1' and server_receipt.get('complete') and server_receipt.get('stack_closed') and server_receipt.get('inputs_preserved') and not server_receipt['errors'], 'whole server receipt')
        need(server_receipt['context_before'] == server_receipt['context_after'] and server_receipt['forced_gc_invocations'] == 0 and server_receipt['requests_issued_by_server'] == 0, 'whole unchanged fixed context and no server request driver')
        need(client_receipt.get('schema') == 'round1789-client-receipt-v1' and client_receipt.get('complete') and client_receipt.get('inputs_preserved') and not client_receipt['errors'], 'whole client receipt')
        for receipt in (server_receipt, client_receipt):
            need(all(receipt[k] == sm[k] for k in ('purpose', 'cohort', 'variant', 'server_binary', 'context', 'population')), 'receipt exact manifest binding')
            for spec in receipt['protected_inputs']:
                verify(spec)
        count, rejects, warm, measured = support.COUNTS[sm['cohort']]
        need(client_receipt['expected_reverts'] == rejects, 'all reject positions included')
        if sm['purpose'] == 'qualification':
            need(client_receipt['response_count'] == count and len(client_receipt['responses']) == count and client_receipt['functional_verification_extra_rpc_count'] == count - rejects, 'whole once-functional population and all positive budget calls')
        else:
            need(client_receipt['response_count'] == count * (warm + measured) and client_receipt['functional_verification_extra_rpc_count'] == 0 and client_receipt['all_responses_validated_after_end'], 'whole fixed paid population with no extra calls')
            need(client_receipt['measured_http_latency']['count'] == count * measured, 'all measured HTTP observations')
            begin, end = server_receipt['begin'], server_receipt['end']
            delta_user = end['cpu_before']['user_ns'] - begin['cpu_after']['user_ns']
            delta_system = end['cpu_before']['system_ns'] - begin['cpu_after']['system_ns']
            need(delta_user >= 0 and delta_system >= 0, 'monotonic fenced server CPU counters')
            report['fenced_server_cpu'] = {'user_ns': delta_user, 'system_ns': delta_system, 'total_ns': delta_user + delta_system, 'boundary': 'post-BEGIN snapshot to pre-END snapshot; raw fence observer costs retained separately, no estimated subtraction'}
            report['normal_gc_collections_between_fences'] = end['normal_gc_heap']['num_gc'] - begin['normal_gc_heap']['num_gc']
            report['gc_amortization_resolved'] = report['normal_gc_collections_between_fences'] >= 2
            report['normal_gc_heap_begin'] = begin['normal_gc_heap']
            report['normal_gc_heap_end'] = end['normal_gc_heap']
            report['measured_http_latency'] = client_receipt['measured_http_latency']
            report['measured_batch_wall_ns'] = client_receipt['measured_batch_wall_ns']
        report['server_receipt'] = identity(server_receipt_path)
        report['client_receipt'] = identity(client_receipt_path)
        report['response_stream'] = client_receipt['response_stream']
        verify(report['response_stream'])
        for spec in protected:
            verify(spec)
        success = True
    except Exception as error:
        report['errors'].append({'error': str(error), 'traceback': traceback.format_exc()})
    finally:
        # Session cleanup uses the wait4 adapter and verifies the process start time,
        # preserving rusage when a failure requires forced termination.
        for label, child in (('client', client), ('server', server)):
            if child is not None:
                try:
                    retired = lifecycle_module.retire(child)
                    report[label + '_retirement'] = retired
                    need(not retired['remaining'], 'fully retired ' + label + ' session')
                except Exception as error:
                    report['errors'].append({'error': label + ' retirement: ' + str(error), 'traceback': traceback.format_exc()})
        if server is not None:
            report['whole_process_server_rusage'] = server.usage
            if server.usage is None:
                report['errors'].append({'error': 'missing exclusive server wait4 rusage'})
        for control in (client_ctl, server_ctl):
            if control is not None:
                try:
                    control.close()
                except Exception as error:
                    report['errors'].append({'error': 'control close: ' + str(error)})
        for f in handles:
            f.close()
        try:
            for spec in protected:
                verify(spec)
        except Exception as error:
            report['errors'].append({'error': 'protected helper inputs: ' + str(error)})
    report['complete'] = success and not report['errors']
    if report['complete'] and sm['purpose'] == 'qualification':
        q = {'schema': 'round1789-qualification-v1', 'complete': True, 'variant': sm['variant'], 'cohort': sm['cohort'], 'context': sm['context'], 'population': sm['population'], 'server_binary': sm['server_binary'], 'server_closed': True, 'response_count': client_receipt['response_count'], 'expected_reverts': client_receipt['expected_reverts'], 'functional_verification_extra_rpc_count': client_receipt['functional_verification_extra_rpc_count'], 'responses': client_receipt['responses'], 'server_receipt': report['server_receipt'], 'client_receipt': report['client_receipt'], 'response_stream': report['response_stream'], 'baseline_qualification': cm['baseline_qualification'], 'whole_process_server_rusage': server.usage, 'server_retirement': report['server_retirement'], 'client_retirement': report['client_retirement'], 'full_state_equivalence_claimed': False, 'global_minimum_claimed': False, 'performance_measured': False}
        report['qualification'] = write(str(out / 'qualification.json'), q)
    report['receipt'] = str(out / 'receipt.json')
    write(report['receipt'], report)
    return report
