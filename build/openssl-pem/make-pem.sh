mkdir keys
cd keys

# generate a self signed certificate for the CA along with a key
mkdir -p ca/private
chmod 700 ca/private
openssl req -x509 -nodes -days 3650 -newkey rsa:4096 -keyout ca/private/ca_key.pem -out ca/ca_cert.pem -subj "/C=US/ST=CA/L=Pleasanton/0=ExampleCompany/CN=example.com"

# create server private key and certificate request
mkdir -p server/private
chmod 700 ca/private
openssl genrsa -out server/private/server_key.pem 4096
openssl req -new -key server/private/server_key.pem -out server/server.csr -subj "/C=US/ST=CA/L=Pleasanton/0=ExampleCompany/CN=server.example.com"

# create client private key and certificate request
mkdir -p client/private
chmod 700 client/private
openssl genrsa -out client/private/client_key.pem 4096
openssl req -new -key client/private/client_key.pem -out client/client.csr -subj "/C=US/ST=CA/L=Pleasanton/0=ExampleCompany/CN=client.example.com"

# generate certificates
openssl x509 -req -days 3650 -in server/server.csr -CA ca/ca_cert.pem -CAkey ca/private/ca_key.pem -CAcreateserial -out server/server_cert.pem
openssl x509 -req -days 3650 -in client/client.csr -CA ca/ca_cert.pem -CAkey ca/private/ca_key.pem -CAcreateserial -out clent/client_cert.pem
