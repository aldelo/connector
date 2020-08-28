echo ======== SERVER CERT PEM ========
openssl x509 -in ./keys/server/server_cert.pem -noout -text
echo .
echo .
echo .

echo ======== SERVER CSR ========
openssl req -in ./keys/server/server.csr -noout -text
echo .
echo .
echo .

echo ======== CLIENT CERT PEM ========
openssl x509 -in ./keys/client/client_cert.pem -noout -text
echo .
echo .
echo .

echo ======== CLIENT CSR ========
openssl req -in ./keys/client/client.csr -noout -text
echo .
echo .
echo .

echo ======== CA CERT PEM ========
openssl x509 -in ./keys/ca/ca_cert.pem -noout -text
echo .
echo .
echo .
