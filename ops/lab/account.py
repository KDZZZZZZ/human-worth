#!/usr/bin/env python3
"""Audited account maintenance in kind-lab; requires its operator kubeconfig."""
import argparse
import json
import secrets

from app import STATE, apply, kube, maintenance_job


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--account',required=True)
    parser.add_argument('--role',required=True,choices=['user','admin'])
    parser.add_argument('--state',required=True,choices=['active','disabled'])
    parser.add_argument('--expected-version',required=True,type=int)
    parser.add_argument('--operator',required=True,help='trusted operator identity for the audit record')
    options=parser.parse_args()
    build=json.loads((STATE/'build.json').read_text())
    name='identity-admin-'+secrets.token_hex(5)
    args=['--account',options.account,'--role',options.role,'--state',options.state,'--expected-version',str(options.expected_version)]
    obj=maintenance_job(name,'identity-admin',build['images']['identity-admin']['tag'],args,'identity-operator',[{'name':'OPERATOR_IDENTITY','value':options.operator}])
    apply(json.dumps(obj))
    try:
        kube('-n','human-worth','wait','--for=condition=Complete','job/'+name,'--timeout=140s')
    finally:
        kube('-n','human-worth','logs','job/'+name)


if __name__=='__main__':
    main()
