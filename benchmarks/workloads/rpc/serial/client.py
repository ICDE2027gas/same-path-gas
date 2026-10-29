"""Define one HTTP client process, launched through the explicit main entry point."""
import base64
import copy
import hashlib
import http.client
import json
import os
import sys
import time
import traceback
from pathlib import Path

ROOT = Path(os.environ['GAS_CERTIFICATE_BENCH_ROOT']).resolve(strict=True)
LIMIT = 64 << 20
BODY_LIMIT = 8 << 20
BUFFER_LIMIT = 64 << 20
COUNTS = {'E': (9, 2, 16, 4096), 'A': (188, 11, 1, 256)}


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


class Client:
    def __init__(self, manifest_path):
        self.manifest_spec = identity(manifest_path)
        self.m = m = read(manifest_path)
        need(m.get('schema') == 'round1789-client-input-v1', 'released concrete client manifest')
        self.sm = sm = load(m['server_manifest'])
        need(sm['schema'] == 'round1789-server-input-v1', 'paired server manifest')
        need(sm['purpose'] in ('qualification', 'paid') and sm['cohort'] in COUNTS and sm['variant'] in ('P', 'S', 'C', 'V'), 'declared purpose/cohort/variant')
        need(m['response_body_cap'] == BODY_LIMIT and m['http_timeout_seconds'] == 120, 'fixed bounded ordinary HTTP settings')
        self.protected = [self.manifest_spec, m['server_manifest'], sm['server_binary'], sm['context'], sm['population']]
        self.context, self.p = population(sm['population'], sm['context'], sm['cohort'])
        self.expected = self.baseline = None
        if sm['purpose'] == 'paid':
            need(sm['qualification'] is not None and m['baseline_qualification'] is None, 'paid exact variant qualification only')
            self.protected.append(sm['qualification'])
            self.expected = qualification(sm['qualification'], m, sm, self.p)
        elif sm['variant'] == 'P':
            need(m['baseline_qualification'] is None, 'P establishes same-context baseline')
        else:
            need(m['baseline_qualification'] is not None, 'S/C require completed pristine same-context proof')
            self.protected.append(m['baseline_qualification'])
            self.baseline = qualification(m['baseline_qualification'], m, sm, self.p, baseline=True)
        count, _, warm, measured = COUNTS[sm['cohort']]
        expected_passes = (warm, measured) if sm['purpose'] == 'paid' else (0, 0)
        need((m['warmup_passes'], m['measured_passes']) == expected_passes, 'frozen complete pass counts')
        for s in self.protected:
            verify(s)
        self.out = path(m['output_directory'], exists=False)
        self.out.mkdir(mode=0o700)
        self.preencoded = [compact(e['estimate_request']) for e in self.p['entries']]
        self.rows = [None] * (count * (warm + measured) if sm['purpose'] == 'paid' else count)
        self.calls = []
        self.used, self.batch_wall_ns, self.errors = 0, None, []
        self.buffered_bytes = 0
        self.connection = http.client.HTTPConnection('127.0.0.1', sm['http_port'], timeout=120)
        self.connection.connect()  # Establish TCP outside request timers, with RPC requests reserved for the workload.

    def exchange(self, raw):
        begin = time.perf_counter_ns()
        self.connection.request('POST', '/', body=raw, headers={'Content-Type': 'application/json'})
        response = self.connection.getresponse()
        body = response.read(BODY_LIMIT + 1)
        end = time.perf_counter_ns()
        return body, response.status, end - begin

    def passes(self, label, passes):
        start = time.perf_counter_ns()
        for n in range(passes):
            for position, raw in enumerate(self.preencoded):
                body, status, elapsed = self.exchange(raw)
                self.rows[self.used] = (label, n, position, body, status, elapsed)
                self.used += 1
                self.buffered_bytes += len(body)
                need(self.buffered_bytes <= BUFFER_LIMIT, 'fixed aggregate raw-response buffer cap')
                # Buffer raw responses here. JSON decoding, validation, hashing, and file writes
                # occur outside the timed loop.
        return time.perf_counter_ns() - start

    def qualify(self):
        self.passes('qualification', 1)
        results = []
        for i, e in enumerate(self.p['entries']):
            body, status = self.rows[i][3:5]
            reply, value = checked_response(body, status, e['estimate_request'])
            original = payload(e['original_response'], e['original_request'])
            if e['original_reverted'] or self.sm['variant'] in ('P', 'S'):
                need(value == original, 'exact original stock payload except id at position ' + str(i))
            row = {'position': i, 'original_sequence': e['original_sequence'], 'estimate_request': e['estimate_request'], 'estimate_response': reply, 'payload': value, 'functional_eth_call': None}
            if not e['original_reverted']:
                need('result' in value and isinstance(value['result'], str) and value['result'].startswith('0x'), 'positive returned budget')
                budget = int(value['result'], 16)
                need(0 < budget <= self.context['rpc_gas_cap'], 'returned gas budget in recorded cap')
                request = copy.deepcopy(e['estimate_request'])
                request['method'] = 'eth_call'
                request['params'][0]['gas'] = value['result']
                raw, status, elapsed = self.exchange(compact(request))
                self.calls.append((i, request, raw, status, elapsed))
                self.buffered_bytes += len(raw)
                need(self.buffered_bytes <= BUFFER_LIMIT, 'fixed aggregate raw-response buffer cap')
                response, call_value = checked_response(raw, status, request)
                need('result' in call_value and isinstance(call_value['result'], str) and call_value['result'].startswith('0x'), 'functional same-context budget sufficiency, including deferred state reads')
                if self.baseline is not None:
                    want = self.baseline['responses'][i]['functional_eth_call']
                    need(want is not None and call_value == want['payload'], 'same-context successful return bytes equal pristine verification')
                row['functional_eth_call'] = {'request': request, 'response': response, 'payload': call_value, 'kind': 'functional_verification_extra_rpc'}
            results.append(row)
        return results

    def finalize(self, qualification_rows=None):
        # The controller sends FINALIZE only after server END in paid mode.
        raw_file = self.out / 'responses.jsonl'
        results = [] if qualification_rows is None else qualification_rows
        latencies = []
        with raw_file.open('xb') as f:
            for stored in self.rows[:self.used]:
                label, pass_number, position, body, status, elapsed = stored
                record = {'kind': 'estimate', 'phase': label, 'pass': pass_number, 'position': position, 'http_status': status, 'http_elapsed_ns': elapsed, 'raw_response_base64': base64.b64encode(body).decode()}
                f.write(compact(record) + b'\n')
            for i, request, body, status, elapsed in self.calls:
                f.write(compact({'kind': 'functional_verification_extra_rpc', 'position': i, 'request': request, 'http_status': status, 'http_elapsed_ns': elapsed, 'raw_response_base64': base64.b64encode(body).decode()}) + b'\n')
        if self.sm['purpose'] == 'paid':
            for label, _, position, body, status, elapsed in self.rows[:self.used]:
                e = self.p['entries'][position]
                _, value = checked_response(body, status, e['estimate_request'])
                need(value == self.expected['responses'][position]['payload'], 'exact prequalified variant payload at every pass/position')
                if label == 'measured':
                    latencies.append(elapsed)
        for s in self.protected:
            verify(s)
        complete_count = self.p['count'] * (self.m['warmup_passes'] + self.m['measured_passes']) if self.sm['purpose'] == 'paid' else self.p['count']
        need(self.used == complete_count, 'whole cohort/pass completion')
        ordered = sorted(latencies)
        distribution = None if not ordered else {'count': len(ordered), 'total_ns': sum(ordered), 'min_ns': ordered[0], 'max_ns': ordered[-1], 'p50_ns': ordered[(len(ordered) - 1) * 50 // 100], 'p95_ns': ordered[(len(ordered) - 1) * 95 // 100], 'p99_ns': ordered[(len(ordered) - 1) * 99 // 100]}
        report = {'schema': 'round1789-client-receipt-v1', 'complete': True, 'purpose': self.sm['purpose'], 'variant': self.sm['variant'], 'cohort': self.sm['cohort'], 'server_binary': self.sm['server_binary'], 'context': self.sm['context'], 'population': self.sm['population'], 'response_count': self.used, 'expected_reverts': self.p['expected_reverts'], 'warmup_passes': self.m['warmup_passes'], 'measured_passes': self.m['measured_passes'], 'measured_batch_wall_ns': self.batch_wall_ns, 'measured_http_latency': distribution, 'response_stream': identity(str(raw_file)), 'buffered_raw_response_bytes': self.buffered_bytes, 'total_raw_response_cap_bytes': BUFFER_LIMIT, 'functional_verification_extra_rpc_count': len(self.calls), 'responses': results, 'full_state_equivalence_claimed': False, 'global_minimum_claimed': False, 'performance_measured': self.sm['purpose'] == 'paid', 'all_responses_validated_after_end': self.sm['purpose'] == 'paid', 'protected_inputs': self.protected, 'inputs_preserved': True, 'errors': []}
        write(str(self.out / 'receipt.json'), report)
        return report

    def failure(self, error):
        # Invalid units preserve all completed bodies and remain ineligible for measurement.
        stream = self.out / 'failure-responses.jsonl'
        with stream.open('xb') as f:
            for row in self.rows[:self.used]:
                label, number, position, body, status, elapsed = row
                f.write(compact({'phase': label, 'pass': number, 'position': position, 'http_status': status, 'http_elapsed_ns': elapsed, 'raw_response_base64': base64.b64encode(body).decode()}) + b'\n')
            for i, request, body, status, elapsed in self.calls:
                f.write(compact({'kind': 'functional_verification_extra_rpc', 'position': i, 'request': request, 'http_status': status, 'http_elapsed_ns': elapsed, 'raw_response_base64': base64.b64encode(body).decode()}) + b'\n')
        write(str(self.out / 'failure.json'), {'schema': 'round1789-client-failure-v1', 'complete': False, 'error': str(error), 'traceback': traceback.format_exc(), 'completed_estimates': self.used, 'buffered_raw_response_bytes': self.buffered_bytes, 'total_raw_response_cap_bytes': BUFFER_LIMIT, 'functional_verification_extra_rpc_count': len(self.calls), 'response_stream': identity(str(stream))})

    def run(self):
        emit('READY', pid=os.getpid(), purpose=self.sm['purpose'], variant=self.sm['variant'], cohort=self.sm['cohort'], server_manifest=self.m['server_manifest'])
        try:
            if self.sm['purpose'] == 'qualification':
                need(command() == 'QUALIFY', 'one whole qualification command')
                report = self.finalize(self.qualify())
                emit('QUALIFIED', response_count=report['response_count'], functional_verification_extra_rpc_count=len(self.calls))
            else:
                need(command() == 'WARMUP', 'complete warmup first')
                self.passes('warmup', self.m['warmup_passes'])
                emit('WARMUP_DONE', response_count=self.used)
                need(command() == 'MEASURE', 'measurement after server BEGIN')
                self.batch_wall_ns = self.passes('measured', self.m['measured_passes'])
                emit('MEASURED', response_count=self.used)
                need(command() == 'FINALIZE', 'validation/write after server END')
                self.finalize()
                emit('FINALIZED', response_count=self.used)
            return 0
        except Exception as error:
            self.failure(error)
            emit('FAILED', error=str(error))
            return 2
        finally:
            self.connection.close()


def main():
    need(len(sys.argv) == 3 and sys.argv[1] == '--manifest', 'usage: client.py --manifest ACTUAL_RELEASED_MANIFEST')
    return Client(str(path(sys.argv[2]))).run()


if __name__ == '__main__':
    raise SystemExit(main())
