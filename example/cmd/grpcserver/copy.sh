#!/bin/bash

mkdir ~/test
mkdir ~/test/grpcserver1
mkdir ~/test/grpcserver1/x509
mkdir ~/test/grpcserver2
mkdir ~/test/grpcserver2/x509
mkdir ~/test/grpcserver3
mkdir ~/test/grpcserver3/x509
mkdir ~/test/grpcserver4
mkdir ~/test/grpcserver4/x509
mkdir ~/test/grpcserver5
mkdir ~/test/grpcserver5/x509
mkdir ~/test/grpcserver6
mkdir ~/test/grpcserver6/x509
mkdir ~/test/grpcserver7
mkdir ~/test/grpcserver7/x509
mkdir ~/test/grpcserver8
mkdir ~/test/grpcserver8/x509
mkdir ~/test/grpcserver9
mkdir ~/test/grpcserver9/x509
mkdir ~/test/grpcserver10
mkdir ~/test/grpcserver10/x509
mkdir ~/test/grpcclient
mkdir ~/test/grpcclient/x509
mkdir ~/test/grpcclient/endpoint

cp grpcserver ~/test/grpcserver1/grpcserver
cp service.yaml ~/test/grpcserver1/service.yaml
cp -R x509 ~/test/grpcserver1
cp grpcserver ~/test/grpcserver2/grpcserver
cp service.yaml ~/test/grpcserver2/service.yaml
cp -R x509 ~/test/grpcserver2
cp grpcserver ~/test/grpcserver3/grpcserver
cp service.yaml ~/test/grpcserver3/service.yaml
cp -R x509 ~/test/grpcserver3
cp grpcserver ~/test/grpcserver4/grpcserver
cp service.yaml ~/test/grpcserver4/service.yaml
cp -R x509 ~/test/grpcserver4
cp grpcserver ~/test/grpcserver5/grpcserver
cp service.yaml ~/test/grpcserver5/service.yaml
cp -R x509 ~/test/grpcserver5
cp grpcserver ~/test/grpcserver6/grpcserver
cp service.yaml ~/test/grpcserver6/service.yaml
cp -R x509 ~/test/grpcserver6
cp grpcserver ~/test/grpcserver7/grpcserver
cp service.yaml ~/test/grpcserver7/service.yaml
cp -R x509 ~/test/grpcserver7
cp grpcserver ~/test/grpcserver8/grpcserver
cp service.yaml ~/test/grpcserver8/service.yaml
cp -R x509 ~/test/grpcserver8
cp grpcserver ~/test/grpcserver9/grpcserver
cp service.yaml ~/test/grpcserver9/service.yaml
cp -R x509 ~/test/grpcserver9
cp grpcserver ~/test/grpcserver10/grpcserver
cp service.yaml ~/test/grpcserver10/service.yaml
cp -R x509 ~/test/grpcserver10

cd ..
cd grpcclient

cp grpcclient ~/test/grpcclient/
cp -R endpoint ~/test/grpcclient/
cp -R x509 ~/test/grpcclient/
