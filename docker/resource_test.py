#!/usr/bin/env python3
"""Real per-node resource acceptance through the HTTP/Gateway/Controller path.

Run only via resource_lab.py resource-test against its fresh isolated stack.
Configuration is never injected into the reference sevpn/sdn-docker environment.
"""
import copy
import importlib.util
import json
import time
import urllib.parse
import uuid

import manage


def operation(code, action, field, **data):
    return dict(code=code, action=action, **{field: data})


def main():
    if manage.PROJECT != 'sdn-resource-it':
        raise RuntimeError('Resource acceptance requires the isolated resource_lab.py entry point')
    spec = importlib.util.spec_from_file_location('resource_http', manage.ROOT / 'lab/run.py')
    lab = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(lab)
    values, tokens = manage.initialize()
    lab.ART = manage.ART
    lab.RESULT_FILE = 'resource-results.json'
    lab.BASE = 'http://127.0.0.1:'+values['SDN_WEB_PORT']+'/api/v1'
    lab.TOKENS = tokens
    lab.exe = manage.exe
    request, passed, wait = lab.request, lab.passed, lab.wait
    nodes = {}

    def begin():
        return request('/changes', 'POST', {'baseRevision': request('/desired')['revision']},
                       key=str(uuid.uuid4()), expected=201)

    def edit(candidate, node, operations=(), policies=()):
        return request('/changes/'+candidate['id']+'/nodes/'+nodes[node]+'/resources', 'PUT',
                       dict(editRevision=candidate['editRevision'], operations=operations, bridgePolicies=policies))

    def submit(edits, policies=None, expected='SUCCEEDED'):
        candidate = begin()
        for node in dict.fromkeys(list(edits)+list(policies or {})):
            candidate = edit(candidate, node, edits.get(node, []), (policies or {}).get(node, []))
        plan = request('/changes/'+candidate['id']+'/plan', 'POST')
        result, replay = lab.commit(candidate, plan, expected_state=expected)
        return result, plan

    def query(node, kind, **filters):
        result = request('/nodes/'+nodes[node]+'/query?'+urllib.parse.urlencode(dict(kind=kind, **filters)))
        assert result.get('nodeId') == nodes[node], result
        assert result.get('source') and int(result.get('collectedUnixMs', 0)) > 0, result
        assert not result.get('error') and not result.get('stale'), result
        return result

    def assert_contains(result, value):
        assert value in json.dumps(result, ensure_ascii=False), (value, result)

    def reject_edit(node, ops):
        candidate = begin()
        result = request('/changes/'+candidate['id']+'/nodes/'+nodes[node]+'/resources', 'PUT',
                         dict(editRevision=candidate['editRevision'], operations=ops), expected=409)
        request('/changes/'+candidate['id'], 'DELETE')
        return result

    try:
        manage.verify_clean()
        assert all(manage.running(n) for n in manage.LAB_SERVICES), 'lab-up is required'
        nodes = {n['endpoint'].split(':')[0]: n['nodeId'] for n in manage.wait_ready()}
        lab.save('resource-nodes.json', nodes)

        # Existing Agent configuration is imported only after actual-state checks.
        direct = dict(operations=[dict(id='import-vrf', **operation('TENANT', 'CREATE', 'tenant', name='IMPORT-A', tableId=1901))])
        source = manage.STATE / 'import.json'
        source.write_text(json.dumps(direct))
        manage.owned('spine')
        manage.run('docker', 'cp', source, manage.PROJECT+'-spine:/tmp/resource-import.json')
        manage.exe('spine', 'sdnctl', '-insecure', '-file', '/tmp/resource-import.json', 'submit')
        state = json.loads(manage.exe('spine', 'sdnctl', '-insecure', 'state').stdout)
        revision = str(state['revision'])
        request('/nodes/'+nodes['spine']+'/resources/import', 'POST', dict(expectedAgentRevision='0'),
                key=str(uuid.uuid4()), expected=409)
        imported = request('/nodes/'+nodes['spine']+'/resources/import', 'POST', dict(expectedAgentRevision=revision), key=str(uuid.uuid4()))
        assert_contains(imported, 'IMPORT-A')
        assert imported['adopted']
        result, plan = submit({'spine': [operation('TENANT', 'CREATE', 'tenant', name='IMPORT-A', tableId=1901)]})
        assert not plan['nodes'], plan
        submit({'spine': [operation('TENANT', 'DELETE', 'tenant', name='IMPORT-A')]})
        assert 'IMPORT-A' not in json.dumps(query('spine', 'TENANTS'))
        passed('existing Agent VRF is verified/imported; repeated create is a no-op and deletion is managed')

        for name in ('leaf1', 'leaf2'):
            request('/nodes/'+nodes[name]+'/adopt', 'POST', dict(expectedRevision='0', allowExisting=False))
        config = json.loads((manage.STATE / 'evpn.json').read_text())
        candidate, plan = lab.candidate(config)
        # Use the established reference topology, but apply its independent
        # objects incrementally through the new resource editing API.
        by_id = {value: key for key, value in nodes.items()}
        targets = {by_id[item['nodeId']]: copy.deepcopy(item['targetOperations']) for item in plan['nodes']}
        request('/changes/'+candidate['id'], 'DELETE')
        lab.save('resource-reference-targets.json', targets)
        for ops in targets.values():
            for item in ops:
                item.pop('id', None)
                item.pop('dependsOn', None)
                if item['code'] == 'NODE_NETWORK':
                    for setting in item['network']['settings']:
                        if setting['key'].endswith('disable_ipv6'):
                            setting['value'] = '0'
                    item['network']['settings'].append(dict(key='net.ipv6.conf.all.forwarding', value='1'))

        def selected(predicate):
            return {node: [item for item in ops if predicate(item)] for node, ops in targets.items()}

        submit(selected(lambda o: o['code'] == 'TENANT'))
        for node in ('leaf1', 'leaf2'):
            assert_contains(query(node, 'TENANTS'), 'TENANT-A')
            assert not query(node, 'BRIDGES')['records']
        passed('empty VRF can be created first without creating any BD or VXLAN')

        submit(selected(lambda o: o['code'] in ('BD', 'NODE_NETWORK')))
        policies = {'leaf1': [dict(bridge='br10', neighSuppress=True)], 'leaf2': [dict(bridge='br20', neighSuppress=True)]}
        submit({}, policies)
        for node in ('leaf1', 'leaf2'):
            assert not query(node, 'VXLAN_TUNNELS')['records']
        passed('standalone BD and ARP/ND suppression intent persist before a VXLAN exists')

        submit(selected(lambda o: o['code'] == 'VXLAN'))
        submit(selected(lambda o: o['code'] in ('BD_TENANT', 'BD_PORT', 'INTERFACE', 'ADDRESS')))
        submit(selected(lambda o: o['code'] == 'TENANT_L3VNI'))
        for node, suffix in (('leaf1', '10'), ('leaf2', '20')):
            assert_contains(query(node, 'BRIDGES', vrf='TENANT-A'), 'br'+suffix)
            ports = query(node, 'PORTS', bridge='br'+suffix)
            assert_contains(ports, 'vx100'+suffix)
            actual = manage.exe(node, 'bridge', '-d', 'link', 'show', 'dev', 'vx100'+suffix).stdout
            assert 'neigh_suppress on' in actual, actual
        passed('BD/VRF/ports/addresses/VXLAN/L3VNI are separate transactions; new tunnel inherits BD suppression')

        protocol_codes = {'OSPF_INSTANCE', 'OSPF_NETWORK', 'OSPF_INTERFACE', 'BGP_INSTANCE', 'BGP_NEIGHBOR',
                          'BGP_EVPN_PEER', 'BGP_EVPN_GLOBAL', 'EVPN_L2VNI', 'EVPN_TENANT'}
        submit(selected(lambda o: o['code'] in protocol_codes))
        manage.hosts()
        lab.forwarding()
        passed('per-resource provisioning establishes the reference OSPF/BGP EVPN topology and IPv4 symmetric IRB traffic')

        # Presence-aware updates and guarded dependency removal.
        submit({'leaf1': [operation('BD', 'SET', 'bridge', name='br10', ageingTime=0)]},
               {'leaf1': [dict(bridge='br10', neighSuppress=False)]})
        actual = manage.exe('leaf1', 'bridge', '-d', 'link', 'show', 'dev', 'vx10010').stdout
        assert 'neigh_suppress off' in actual, actual
        desired = request('/nodes/'+nodes['leaf1']+'/resources')
        bd = next(o['bridge'] for o in desired['resources']['objects'] if o['code'] == 'BD' and o['bridge']['name'] == 'br10')
        assert bd['ageingTime'] == 0 and bd['mtu'] == 1500, bd
        reject_edit('leaf1', [operation('TENANT', 'DELETE', 'tenant', name='TENANT-A')])
        reject_edit('leaf1', [operation('BD', 'DELETE', 'bridge', name='br10')])
        submit({'leaf1': [operation('BD', 'SET', 'bridge', name='br10', ageingTime=300)]},
               {'leaf1': [dict(bridge='br10', neighSuppress=True)]})
        passed('false/zero updates preserve omitted fields; referenced VRF/BD deletion is rejected before commit')

        before = json.loads(manage.exe('leaf1', 'sdnctl', '-insecure', 'state').stdout)['revision']
        job, _ = submit({'leaf1': [operation('BD', 'CREATE', 'bridge', name='br-fail'),
                                  operation('BD_PORT', 'BIND', 'bridgePort', bridge='br-fail', port='absent-port')]}, expected='FAILED')
        assert job['nodes'][0]['state'] == 'FAILED_ROLLED_BACK', job
        assert json.loads(manage.exe('leaf1', 'sdnctl', '-insecure', 'state').stdout)['revision'] == before
        assert manage.exe('leaf1', 'ip', 'link', 'show', 'br-fail', check=False).returncode != 0
        assert_contains(request('/desired'), 'br-fail')
        submit({'leaf1':[operation('BD_PORT', 'UNBIND', 'bridgePort', bridge='br-fail', port='absent-port'),
                         operation('BD', 'DELETE', 'bridge', name='br-fail')]})
        passed('real missing-port failure rolls back actual BD and Agent revision; failed desired intent can be corrected in the next candidate')

        ipv6 = {}
        for node in ('spine', 'leaf1', 'leaf2'):
            index = 0 if node == 'spine' else int(node[-1])
            router = '10.255.0.'+('1' if not index else str(10+index))
            addresses = [('lo', '2001:db8:ff::'+('1' if not index else str(10+index))+'/128')]
            uplinks = ['swp1', 'swp2'] if not index else ['uplink']
            if not index:
                addresses += [('swp1', '2001:db8:1::1/64'), ('swp2', '2001:db8:2::1/64')]
            else:
                addresses += [('uplink', f'2001:db8:{index}::2/64'), ('br'+str(index*10), f'2001:db8:{index*10}::fe/64')]
            ops = [operation('ADDRESS', 'CREATE', 'address', interface=iface, prefix=prefix) for iface, prefix in addresses]
            if index:
                # The IPv4 reference disables autoconfig on SVIs. IPv6 host
                # discovery requires an SVI link-local source for outbound NS.
                # The addressless L3VNI SVI remains addrgenmode=none.
                ops.append(operation('INTERFACE', 'SET', 'interface', name='br'+str(index*10), addrgenmode='eui64'))
                # Changing addrgenmode on an already-UP SVI does not create an
                # address immediately. Add its MAC-derived link-local without
                # flapping the existing IPv4 service.
                ops.append(operation('ADDRESS', 'CREATE', 'address', interface='br'+str(index*10), prefix=f'fe80::ff:fe00:{index*10}/64'))
            ops.append(operation('OSPF6_INSTANCE', 'CREATE', 'ospf', routerId=router))
            ops += [operation('OSPF6_INTERFACE', 'SET', 'ospf6Interface', name=iface, area='0.0.0.0', networkType='point-to-point', passive=False) for iface in uplinks]
            ops.append(operation('OSPF6_INTERFACE', 'SET', 'ospf6Interface', name='lo', area='0.0.0.0', passive=True))
            ipv6[node] = ops
        submit(ipv6)
        def ospf6_ready():
            out = manage.exe('spine', 'vtysh', '-c', 'show ipv6 ospf6 neighbor').stdout
            return out.count('Full') >= 2
        wait(ospf6_ready, 'OSPFv3 two Full neighbors')
        for family, version in (('IPV4', 2), ('IPV6', 3)):
            for kind in ('OSPF_SUMMARY', 'OSPF_INTERFACES', 'OSPF_NEIGHBORS', 'OSPF_DATABASE', 'OSPF_ROUTES'):
                lab.save(f'query-{kind}-{version}.json', query('spine', kind, family=family, ospfVersion=version))
        passed('IPv6 addresses and OSPFv3 establish real neighbors; OSPFv2/v3 operational queries succeed')

        type5 = {}
        for node in ('leaf1', 'leaf2'):
            ops = []
            for family in ('IPV4', 'IPV6'):
                ops += [operation('BGP_REDISTRIBUTE', 'CREATE', 'bgpRedistribute', asn=65000, vrf='TENANT-A', family=family, protocol='connected'),
                        operation('EVPN_TYPE5', 'SET', 'bgpType5', asn=65000, vrf='TENANT-A', family=family, enabled=True)]
            type5[node] = ops
        submit(type5)
        def type5_ready():
            out = query('spine', 'EVPN_ROUTES', routeType=5)
            text = json.dumps(out)
            return out if '192.168.10.0' in text and '2001:db8:10::' in text and '192.168.20.0' in text and '2001:db8:20::' in text else None
        lab.save('query-type5.json', wait(type5_ready, 'IPv4 and IPv6 EVPN Type-5'))
        for i in (1, 2):
            pc = 'pc'+str(i)
            manage.exe(pc, 'sysctl', '-w', 'net.ipv6.conf.all.disable_ipv6=0')
            manage.exe(pc, 'sysctl', '-w', 'net.ipv6.conf.default.disable_ipv6=0')
            manage.exe(pc, 'ip', '-6', 'addr', 'add', f'2001:db8:{i*10}::{10+i}/64', 'dev', 'eth0')
            manage.exe(pc, 'ip', '-6', 'route', 'add', 'default', 'via', f'2001:db8:{i*10}::fe', 'dev', 'eth0')
        for i in (1, 2):
            node, bridge, host = 'leaf'+str(i), 'br'+str(i*10), f'2001:db8:{i*10}::{10+i}'
            def link_local_ready(node=node, bridge=bridge):
                rows = json.loads(manage.exe(node, 'ip', '-j', '-6', 'address', 'show', 'dev', bridge).stdout)
                return any(info.get('scope') == 'link' and not info.get('tentative') and not info.get('dadfailed') and 'tentative' not in info.get('flags', []) and 'dadfailed' not in info.get('flags', []) for row in rows for info in row['addr_info'])
            wait(link_local_ready, 'IPv6 business SVI link-local DAD')
            manage.exe(node, 'ip', '-6', 'neigh', 'del', host, 'dev', bridge, check=False)
            assert not manage.exe(node, 'ip', '-6', 'neigh', 'show', 'to', host, 'dev', bridge).stdout.strip()
        for src, dst in (('pc1', '2001:db8:20::12'), ('pc2', '2001:db8:10::11')):
            wait(lambda: manage.exe(src, 'ping', '-6', '-c', '2', '-W', '1', dst, check=False).returncode == 0, 'IPv6 cross-leaf traffic')
        passed('IPv4/IPv6 Type-5 prefix export and bidirectional IPv6 tenant traffic work with cold host neighbor caches')

        unicast = {}
        for node in ('spine', 'leaf1', 'leaf2'):
            peers = [item['peer']['name'] for item in targets[node] if item['code'] == 'BGP_NEIGHBOR']
            ops = []
            for family in ('IPV4', 'IPV6'):
                for peer in peers:
                    ops.append(operation('BGP_AF_PEER', 'SET', 'peer', asn=65000, name=peer, family=family, active=True, rrClient=node == 'spine'))
            if node != 'spine':
                index = int(node[-1])
                ops += [operation('BGP_NETWORK', 'CREATE', 'bgpNetwork', asn=65000, family='IPV4', prefix=f'10.255.0.{10+index}/32'),
                        operation('BGP_NETWORK', 'CREATE', 'bgpNetwork', asn=65000, family='IPV6', prefix=f'2001:db8:ff::{10+index}/128')]
            unicast[node] = ops
        submit(unicast)
        for family, prefix in (('IPV4', '10.255.0.11/32'), ('IPV6', '2001:db8:ff::11/128')):
            def learned(family=family, prefix=prefix):
                result = query('spine', 'BGP_ROUTES', family=family)
                return result if prefix in json.dumps(result) else None
            wait(learned, 'BGP '+family+' unicast route')
        passed('independent BGP IPv4/IPv6 unicast peer activation and network origination learn routes on Spine')

        # Every virtual-object lifecycle is exercised, including deletion of
        # interface attributes associated with a removed virtual device.
        temp = [operation('TENANT', 'CREATE', 'tenant', name='TEMP-A', tableId=1099),
                operation('BD', 'CREATE', 'bridge', name='br-temp', mtu=1500),
                operation('BD_TENANT', 'BIND', 'binding', bridge='br-temp', tenant='TEMP-A'),
                operation('INTERFACE', 'SET', 'interface', name='TEMP-A', up=True),
                operation('INTERFACE', 'SET', 'interface', name='br-temp', up=True, mtu=1500),
                operation('ADDRESS', 'CREATE', 'address', interface='br-temp', prefix='192.0.2.1/24'),
                operation('ADDRESS', 'CREATE', 'address', interface='br-temp', prefix='2001:db8:99::1/64'),
                operation('VXLAN', 'CREATE', 'vxlan', name='vx-temp', vni=10099, local='10.255.0.11', port=4789),
                operation('INTERFACE', 'SET', 'interface', name='vx-temp', up=True),
                operation('BD_PORT', 'BIND', 'bridgePort', bridge='br-temp', port='vx-temp')]
        submit({'leaf1': temp}, {'leaf1':[dict(bridge='br-temp', neighSuppress=True)]})
        submit({'leaf1':[operation('BD', 'SET', 'bridge', name='br-temp', mtu=1400)]})
        submit({'leaf1':[operation('INTERFACE', 'SET', 'interface', name='br-temp', up=True)]})
        actual = json.loads(manage.exe('leaf1', 'ip', '-j', 'link', 'show', 'br-temp').stdout)[0]
        assert actual['mtu'] == 1400, actual
        submit({'leaf1':[operation('ADDRESS', 'DELETE', 'address', interface='br-temp', prefix='192.0.2.1/24'),
                         operation('ADDRESS', 'DELETE', 'address', interface='br-temp', prefix='2001:db8:99::1/64'),
                         operation('BD_PORT', 'UNBIND', 'bridgePort', bridge='br-temp', port='vx-temp'),
                         operation('VXLAN', 'DELETE', 'vxlan', name='vx-temp'),
                         operation('BD_TENANT', 'UNBIND', 'binding', bridge='br-temp', tenant='TEMP-A'),
                         operation('BD', 'DELETE', 'bridge', name='br-temp'),
                         operation('TENANT', 'DELETE', 'tenant', name='TEMP-A')]})
        for name in ('TEMP-A', 'br-temp', 'vx-temp'):
            assert manage.exe('leaf1', 'ip', 'link', 'show', name, check=False).returncode != 0
        assert 'br-temp' not in json.dumps(request('/nodes/'+nodes['leaf1']+'/resources'))
        passed('VRF/BD/address/VXLAN/port CRUD cleans virtual interface attributes; MTU updates do not revert on later link-up')

        query_cases = [('TENANTS', {}), ('BRIDGES', {'vrf':'TENANT-A'}), ('INTERFACES', {}),
                       ('ADDRESSES', {'interface':'br10','family':'IPV6'}), ('ROUTES', {'vrf':'TENANT-A','family':'IPV4'}),
                       ('FDB', {'bridge':'br10'}), ('NEIGHBORS', {'interface':'br10','family':'IPV6'}),
                       ('VXLAN_TUNNELS', {}), ('PORTS', {'bridge':'br10'}), ('BGP_SUMMARY', {'family':'L2VPN_EVPN'}),
                       ('BGP_NEIGHBORS', {}), ('BGP_ROUTES', {'vrf':'TENANT-A','family':'IPV6'}),
                       ('EVPN_ROUTES', {}), ('EVPN_VNI', {})]
        for kind, filters in query_cases:
            lab.save('query-'+kind+'.json', query('leaf1', kind, **filters))
        bad = request('/nodes/'+nodes['leaf1']+'/query?kind=ROUTES&vrf=NONEXIST', expected=409)
        assert bad.get('message'), bad
        request('/nodes/'+nodes['leaf1']+'/query?kind=ROUTES&vfr=TENANT-A', expected=400)
        request('/nodes/'+nodes['leaf1']+'/resources/import', 'POST', dict(expectedAgentRevision='0'), role='viewer', key=str(uuid.uuid4()), expected=403)
        page = query('leaf1', 'INTERFACES', pageSize=1)
        assert page.get('nextPageToken'), page
        page2 = query('leaf1', 'INTERFACES', pageSize=1, pageToken=page['nextPageToken'])
        assert page['records'] != page2['records']
        passed('structured queries carry actual data/source/time; unknown filters, missing VRFs and viewer writes are not silently accepted')

        def cli(*args):
            result = manage.exe('controller', 'sdnc', '-insecure', '-node', nodes['leaf1'], *args)
            return json.loads(result.stdout)
        assert cli('tenant', 'create', 'CLI-TEST', '--table-id', '62002')['state'] == 'SUCCEEDED'
        assert_contains(cli('tenant', 'show', 'CLI-TEST'), 'CLI-TEST')
        change = cli('change', 'begin')['id']
        cli('bd', 'create', 'cli-bd', '--mtu', '1500', '--change', change)
        cli('bd', 'bind-tenant', 'cli-bd', '--tenant', 'CLI-TEST', '--change', change)
        cli('bd', 'address-add', 'cli-bd', '--prefix', '198.51.100.1/24', '--change', change)
        assert manage.exe('leaf1', 'ip', 'link', 'show', 'cli-bd', check=False).returncode != 0
        assert cli('change', 'plan', change)['nodes']
        assert cli('change', 'commit', change)['state'] == 'SUCCEEDED'
        assert_contains(cli('bd', 'addresses', 'cli-bd'), '198.51.100.1')
        change = cli('change', 'begin')['id']
        cli('bd', 'address-delete', 'cli-bd', '--prefix', '198.51.100.1/24', '--change', change)
        cli('bd', 'unbind-tenant', 'cli-bd', '--tenant', 'CLI-TEST', '--change', change)
        cli('bd', 'delete', 'cli-bd', '--change', change)
        cli('tenant', 'delete', 'CLI-TEST', '--change', change)
        assert cli('change', 'commit', change)['state'] == 'SUCCEEDED'
        assert 'CLI-TEST' not in json.dumps(cli('tenant', 'show'))
        passed('named Controller CLI supports single-command VRF commits and explicit multi-operation candidate/plan/commit/delete')

        # Persist desired configuration, ownership and operation results across
        # management restarts without restarting forwarding nodes.
        snapshot = request('/desired')
        for service in ('controller', 'web'):
            manage.owned(service)
            manage.run('docker', 'restart', '--time', '45', manage.PROJECT+'-'+service, timeout=80)
        manage.wait_ready()
        assert request('/desired') == snapshot
        lab.forwarding()
        manage.bundled_check()
        manage.verify_unrelated()
        passed('embedded etcd ownership/desired state survives Controller/Web restart; forwarding and unrelated environments are unchanged')
        lab.save('resource-final-desired.json', request('/desired'))
        print('PASS per-node resource acceptance; isolated lab remains available for browser/CLI checks.', flush=True)
    except Exception as error:
        lab.RESULTS.append(dict(name='per-node resource acceptance', status='FAIL', error=str(error)))
        lab.save(lab.RESULT_FILE, lab.RESULTS)
        raise


if __name__ == '__main__':
    raise SystemExit('Use resource_lab.py resource-test')
