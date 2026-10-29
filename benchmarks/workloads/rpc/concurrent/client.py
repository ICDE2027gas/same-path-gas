"""Define one HTTP client process, launched through the explicit main entry point."""
import base64
import copy
import hashlib
import http.client
import json
import os
import signal
import sys
import time
import traceback
from pathlib import Path

ROOT = Path(os.environ['GAS_CERTIFICATE_BENCH_ROOT']).resolve(strict=True)
LIMIT = 64 << 20
BODY_LIMIT = 8 << 20
BUFFER_LIMIT = 16 << 20
COUNTS = {'E': (9, 2, 4, 1024), 'A': (188, 11, 1, 64)}
CLIENT_CPUS = tuple(map(int, os.environ.get('GAS_CERTIFICATE_CLIENT_CPUS', '8,10,12,14').split(',')))
SERVER_CPUS = tuple(map(int, os.environ.get('GAS_CERTIFICATE_SERVER_CPUS', '0,2,4,6').split(',')))
WORKERS = 4


def need(value, message):
    if not value:
        raise ValueError(message)


def path(value, exists=True):
    p = Path(value)
    need(p.is_absolute() and p != ROOT and ROOT in p.parents, 'absolute ROOT-contained path')
    need(p.resolve(strict=exists) == p, 'canonical path without symlinks')
    return p


def identity(value):
    p = path(value)
    h = hashlib.sha256()
    total = 0
    with p.open('rb') as f:
        while True:
            part = f.read(1 << 20)
            if not part:
                break
            h.update(part)
            total += len(part)
    return {'path': str(p), 'bytes': total, 'sha256': h.hexdigest()}


def verify(spec):
    need(isinstance(spec, dict) and set(spec) == {'path', 'bytes', 'sha256'}, 'whole FileSpec')
    need(identity(spec['path']) == spec, 'input identity: ' + spec['path'])
    return spec


def read(value):
    p = path(value)
    need(p.stat().st_size <= LIMIT, 'whole JSON input cap')
    return json.loads(p.read_bytes())


def load(spec):
    verify(spec)
    return read(spec['path'])


def write(value, data):
    p = path(value, exists=False)
    raw = json.dumps(data, indent=2, ensure_ascii=True, allow_nan=False).encode() + b'\n'
    need(len(raw) <= LIMIT, 'whole JSON output cap')
    with p.open('xb') as f:
        f.write(raw)
    return identity(str(p))


def compact(data):
    return json.dumps(data, separators=(',', ':'), ensure_ascii=True, allow_nan=False).encode()


def emit(kind, **data):
    print(json.dumps({'kind': kind, **data}, separators=(',', ':'), allow_nan=False), flush=True)


def command():
    raw = sys.stdin.buffer.readline((1 << 20) + 1)
    need(raw and len(raw) <= 1 << 20 and raw.endswith(b'\n'), 'bounded newline control command')
    value = json.loads(raw)
    need(set(value) == {'command'}, 'only private command field')
    return value['command']


def payload(value, request):
    need(isinstance(value, dict) and value.get('jsonrpc') == '2.0' and value.get('id') == request.get('id'), 'JSON-RPC response envelope/id')
    need(('result' in value) != ('error' in value), 'exactly result or error')
    return {k: v for k, v in value.items() if k != 'id'}


def population(spec, context_spec, cohort):
    c, p = load(context_spec), load(spec)
    count, rejects, _, _ = COUNTS[cohort]
    need(c.get('schema') == 'round1789-fixed-context-v1' and c.get('cohort') == cohort, 'context schema/cohort')
    need(p.get('schema') == 'round1789-fixed-population-v1' and p.get('cohort') == cohort and p.get('context') == context_spec, 'population context pin')
    need(p.get('count') == count and p.get('expected_reverts') == rejects and len(p.get('entries', [])) == count, 'complete fixed population')
    headers = c['headers']
    need(0 < len(headers) <= 10001, 'bounded whole canonical ancestry')
    for i, h in enumerate(headers):
        need(int(h['number'], 16) == i and h.get('stateRoot') and h.get('hash'), 'complete canonical header/root')
        if i:
            need(h['parentHash'] == headers[i - 1]['hash'], 'connected canonical headers')
    previous, errors = None, 0
    for i, e in enumerate(p['entries']):
        need(e['position'] == i, 'every original estimate position')
        seq = e['original_sequence']
        need(type(seq) is int and (previous is None or seq > previous), 'strict original RPC sequence')
        previous = seq
        request, response, resolved = e['original_request'], e['original_response'], e['resolved_header']
        need(request.get('jsonrpc') == '2.0' and request.get('method') == 'eth_estimateGas' and 'id' in request, 'original request intact')
        args = request.get('params')
        need(isinstance(args, list) and 1 <= len(args) <= 3 and isinstance(args[0], dict), 'ordinary original estimate parameters')
        index = int(resolved['number'], 16)
        need(0 <= index < len(headers) and resolved == headers[index], 'exact full resolved canonical header')
        selector = {'blockHash': resolved['hash'], 'requireCanonical': True}
        transformed = copy.deepcopy(request)
        if len(args) == 1:
            transformed['params'].append(selector)
        else:
            transformed['params'][1] = selector
        need(e['pinned_selector'] == selector and e['estimate_request'] == transformed, 'only block selector substitution')
        need(e['original_selector_omitted'] == (len(args) == 1), 'original omitted-selector marker')
        need(e['original_selector'] == (None if len(args) == 1 else args[1]), 'original selector retained')
        original_payload = payload(response, request)
        reverted = 'error' in original_payload
        need(e['original_reverted'] == reverted, 'whole original error position')
        errors += reverted
    need(errors == rejects, 'all original reject positions preserved')
    return c, p


def checked_response(raw, status, request):
    need(status == 200, 'ordinary successful HTTP transport status')
    need(len(raw) <= BODY_LIMIT, 'whole RPC body cap')
    decoded = json.loads(raw)
    return decoded, payload(decoded, request)


def qualification(spec, m, sm, p, baseline=False):
    q = load(spec)
    need(q.get('schema') == 'round1789-qualification-v1' and q.get('complete') and q.get('server_closed'), 'completed separately retired qualification')
    need(q['cohort'] == sm['cohort'] and q['context'] == sm['context'] and q['population'] == sm['population'], 'same stock fixed-context qualification')
    need(q['response_count'] == p['count'] and q['expected_reverts'] == p['expected_reverts'] and len(q['responses']) == p['count'], 'whole qualified population')
    if baseline:
        need(q['variant'] == 'P', 'pristine same-stock-context verification baseline')
    else:
        need(q['variant'] == sm['variant'] and q['server_binary'] == sm['server_binary'], 'same variant/binary qualification')
    for i, row in enumerate(q['responses']):
        need(row['position'] == i and row['original_sequence'] == p['entries'][i]['original_sequence'], 'all qualified positions in original order')
    return q


def distribution(values):
    ordered = sorted(values)
    need(ordered, 'nonempty complete latency population')
    return {'count': len(ordered), 'total_ns': sum(ordered), 'min_ns': ordered[0], 'max_ns': ordered[-1], 'p50_ns': ordered[(len(ordered) - 1) * 50 // 100], 'p95_ns': ordered[(len(ordered) - 1) * 95 // 100], 'p99_ns': ordered[(len(ordered) - 1) * 99 // 100]}


class Client:
    def __init__(self, manifest_path):
        self.manifest_spec = identity(manifest_path)
        self.m = m = read(manifest_path)
        need(m.get('schema') == 'round1810-client-input-v1', 'root-released concurrent client manifest')
        self.client_id = m['client_id']
        need(type(self.client_id) is int and 0 <= self.client_id < WORKERS and m['worker_count'] == WORKERS and m['client_cpu'] == CLIENT_CPUS[self.client_id], 'fixed client identity and CPU')
        self.affinity = sorted(os.sched_getaffinity(0))
        need(self.affinity == [m['client_cpu']], 'actual client affinity equals frozen assigned core')
        self.sm = sm = load(m['server_manifest'])
        need(sm['schema'] == 'round1789-server-input-v1' and sm['purpose'] == 'paid' and sm['cohort'] in COUNTS and sm['variant'] in ('P', 'S', 'C'), 'unchanged qualified paid server manifest')
        need(m['response_body_cap'] == BODY_LIMIT and m['raw_response_cap_bytes'] == BUFFER_LIMIT and m['http_timeout_seconds'] == 120, 'fixed per-client bounded HTTP/buffer settings')
        need(m['baseline_qualification'] is None and sm['qualification'] is not None, 'exact prequalified variant only; no extra functional RPC')
        self.protected = [self.manifest_spec, m['server_manifest'], sm['server_binary'], sm['context'], sm['population'], sm['qualification']]
        self.context, self.p = population(sm['population'], sm['context'], sm['cohort'])
        self.expected = qualification(sm['qualification'], m, sm, self.p)
        count, _, warm, measured = COUNTS[sm['cohort']]
        need((m['warmup_passes'], m['measured_passes']) == (warm, measured), 'frozen per-client complete passes')
        for s in self.protected:
            verify(s)
        self.out = path(m['output_directory'], exists=False)
        self.out.mkdir(mode=0o700)
        self.preencoded = [compact(e['estimate_request']) for e in self.p['entries']]
        self.rows = [None] * (count * (warm + measured))
        self.used = self.issued = self.buffered_bytes = 0
        self.phase_timings = {}
        self.failing = False
        self.connection = http.client.HTTPConnection('127.0.0.1', sm['http_port'], timeout=120)
        self.connection.connect()  # Establish TCP before READY, with RPC requests reserved for the workload.
        signal.signal(signal.SIGTERM, self.stop)
        signal.signal(signal.SIGINT, self.stop)

    def stop(self, number, _frame):
        if not self.failing:
            raise InterruptedError('owned client signal ' + str(number))

    def exchange(self, raw):
        begin = time.monotonic_ns()
        self.connection.request('POST', '/', body=raw, headers={'Content-Type': 'application/json'})
        response = self.connection.getresponse()
        body = response.read(BODY_LIMIT + 1)
        end = time.monotonic_ns()
        return body, response.status, begin, end

    def passes(self, label, passes):
        start = time.monotonic_ns()
        first = last = None
        completed_before = self.used
        for number in range(passes):
            for position, raw in enumerate(self.preencoded):
                self.issued += 1
                body, status, begin, end = self.exchange(raw)
                self.rows[self.used] = (label, number, position, body, status, end - begin, begin, end)
                self.used += 1
                if first is None:
                    first = begin
                last = end
                self.buffered_bytes += len(body)
                need(self.buffered_bytes <= BUFFER_LIMIT, 'fixed 16MiB per-client complete raw-body cap')
                # Buffer raw responses here. JSON decoding, semantic validation, hashing,
                # file writes, and control IPC occur outside the timed loop.
        timing = {'start_monotonic_ns': start, 'end_monotonic_ns': time.monotonic_ns(), 'first_http_start_monotonic_ns': first, 'last_http_end_monotonic_ns': last, 'response_count': self.used - completed_before}
        self.phase_timings[label] = timing
        return timing

    def record(self, row):
        label, number, position, body, status, elapsed, begin, end = row
        return {'kind': 'estimate', 'client_id': self.client_id, 'phase': label, 'pass': number, 'position': position, 'http_status': status, 'http_elapsed_ns': elapsed, 'http_start_monotonic_ns': begin, 'http_end_monotonic_ns': end, 'raw_response_base64': base64.b64encode(body).decode()}

    def finalize(self):
        # FINALIZE can be received only after every client drains and server END.
        raw_file = self.out / 'responses.jsonl'
        with raw_file.open('xb') as stream:
            for row in self.rows[:self.used]:
                stream.write(compact(self.record(row)) + b'\n')
        count, rejects, warm, measured = COUNTS[self.sm['cohort']]
        need(self.used == self.issued == count * (warm + measured), 'every per-client full pass completed exactly once')
        latencies = []
        for index, row in enumerate(self.rows[:self.used]):
            label, number, position, body, status, elapsed, begin, end = row
            expected_label = 'warmup' if index < count * warm else 'measured'
            offset = index if expected_label == 'warmup' else index - count * warm
            need((label, number, position) == (expected_label, offset // count, offset % count), 'every warmup/measured pass and original position retained')
            need(end >= begin and elapsed == end - begin, 'actual monotonic complete-body HTTP interval')
            entry = self.p['entries'][position]
            _, value = checked_response(body, status, entry['estimate_request'])
            need(value == self.expected['responses'][position]['payload'], 'exact prequalified variant payload at every client/pass/position')
            if label == 'measured':
                latencies.append(elapsed)
        for s in self.protected:
            verify(s)
        need(sorted(os.sched_getaffinity(0)) == self.affinity, 'client affinity unchanged')
        measured_timing = self.phase_timings['measured']
        report = {'schema': 'round1810-client-receipt-v1', 'complete': True, 'purpose': 'paid', 'variant': self.sm['variant'], 'cohort': self.sm['cohort'], 'server_binary': self.sm['server_binary'], 'context': self.sm['context'], 'population': self.sm['population'], 'client_id': self.client_id, 'worker_count': WORKERS, 'requested_client_cpu': self.m['client_cpu'], 'observed_affinity': self.affinity, 'response_count': self.used, 'requests_issued': self.issued, 'expected_reverts_per_pass': rejects, 'warmup_response_count': count * warm, 'measured_response_count': count * measured, 'warmup_passes': warm, 'measured_passes': measured, 'phase_timings': self.phase_timings, 'measured_batch_wall_ns': measured_timing['end_monotonic_ns'] - measured_timing['start_monotonic_ns'], 'clock': 'host time.monotonic_ns', 'measured_http_latency': distribution(latencies), 'response_stream': identity(str(raw_file)), 'buffered_raw_response_bytes': self.buffered_bytes, 'total_raw_response_cap_bytes': BUFFER_LIMIT, 'functional_verification_extra_rpc_count': 0, 'responses': [], 'performance_measured': True, 'all_responses_validated_after_end': True, 'protected_inputs': self.protected, 'inputs_preserved': True, 'errors': []}
        write(str(self.out / 'receipt.json'), report)
        return report

    def failure(self, error):
        self.failing = True  # repeated cleanup signals cannot interrupt prefix serialization
        stream = self.out / 'failure-responses.jsonl'
        with stream.open('xb') as handle:
            for row in self.rows[:self.used]:
                handle.write(compact(self.record(row)) + b'\n')
        write(str(self.out / 'failure.json'), {'schema': 'round1810-client-failure-v1', 'complete': False, 'client_id': self.client_id, 'error': str(error), 'traceback': traceback.format_exc(), 'completed_estimates': self.used, 'requests_issued': self.issued, 'phase_timings': self.phase_timings, 'buffered_raw_response_bytes': self.buffered_bytes, 'total_raw_response_cap_bytes': BUFFER_LIMIT, 'functional_verification_extra_rpc_count': 0, 'response_stream': identity(str(stream)), 'partial_unit_not_eligible': True})

    def run(self):
        try:
            emit('READY', pid=os.getpid(), client_id=self.client_id, observed_affinity=self.affinity, purpose='paid', variant=self.sm['variant'], cohort=self.sm['cohort'], server_manifest=self.m['server_manifest'])
            need(command() == 'WARMUP', 'whole warmup first')
            timing = self.passes('warmup', self.m['warmup_passes'])
            emit('WARMUP_DONE', client_id=self.client_id, response_count=self.used, timing=timing)
            need(command() == 'MEASURE', 'measurement after common server BEGIN')
            timing = self.passes('measured', self.m['measured_passes'])
            emit('MEASURED', client_id=self.client_id, response_count=self.used, timing=timing)
            need(command() == 'FINALIZE', 'deferred validation/write only after server END')
            self.finalize()
            emit('FINALIZED', client_id=self.client_id, response_count=self.used)
            return 0
        except Exception as error:
            self.failure(error)
            emit('FAILED', client_id=self.client_id, error=str(error))
            return 2
        finally:
            self.connection.close()


def main():
    need(len(sys.argv) == 3 and sys.argv[1] == '--manifest', 'usage: client.py --manifest RELEASED_CLIENT_MANIFEST')
    return Client(str(path(sys.argv[2]))).run()


if __name__ == '__main__':
    raise SystemExit(main())
