#!/bin/bash

mkdir ~/test
mkdir ~/test/server1
mkdir ~/test/server1/x509
mkdir ~/test/server2
mkdir ~/test/server2/x509
mkdir ~/test/server3
mkdir ~/test/server3/x509
mkdir ~/test/server4
mkdir ~/test/server4/x509
mkdir ~/test/server5
mkdir ~/test/server5/x509
mkdir ~/test/server6
mkdir ~/test/server6/x509
mkdir ~/test/server7
mkdir ~/test/server7/x509
mkdir ~/test/server8
mkdir ~/test/server8/x509
mkdir ~/test/server9
mkdir ~/test/server9/x509
mkdir ~/test/server10
mkdir ~/test/server10/x509
mkdir ~/test/client
mkdir ~/test/client/x509
mkdir ~/test/client/endpoint

cp server ~/test/server1/server
cp service.yaml ~/test/server1/service.yaml
cp -R x509 ~/test/server1
cp server ~/test/server2/server
cp service.yaml ~/test/server2/service.yaml
cp -R x509 ~/test/server2
cp server ~/test/server3/server
cp service.yaml ~/test/server3/service.yaml
cp -R x509 ~/test/server3
cp server ~/test/server4/server
cp service.yaml ~/test/server4/service.yaml
cp -R x509 ~/test/server4
cp server ~/test/server5/server
cp service.yaml ~/test/server5/service.yaml
cp -R x509 ~/test/server5
cp server ~/test/server6/server
cp service.yaml ~/test/server6/service.yaml
cp -R x509 ~/test/server6
cp server ~/test/server7/server
cp service.yaml ~/test/server7/service.yaml
cp -R x509 ~/test/server7
cp server ~/test/server8/server
cp service.yaml ~/test/server8/service.yaml
cp -R x509 ~/test/server8
cp server ~/test/server9/server
cp service.yaml ~/test/server9/service.yaml
cp -R x509 ~/test/server9
cp server ~/test/server10/server
cp service.yaml ~/test/server10/service.yaml
cp -R x509 ~/test/server10

cd ..
cd client

cp client ~/test/client/
cp -R endpoint ~/test/client/
cp -R x509 ~/test/client/
