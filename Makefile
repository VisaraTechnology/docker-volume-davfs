PLUGIN_NAME=visaratechnology/docker-volume-davfs
PLUGIN_TAG=latest

DEV_DOCKER_IMAGE_NAME = docker-cli-dev$(IMAGE_TAG)
MOUNTS = -v "$(CURDIR)":/go/src/github.com/visaratechnology/docker-volume-davfs

all: clean rootfs create

clean:
	@echo "### rm ./plugin"
	@rm -rf ./plugin

rootfs: clean
	@echo "### docker build: rootfs image with docker-volume-davfs"
	@docker build -q -t ${PLUGIN_NAME}:rootfs .
	@echo "### create rootfs directory in ./plugin/rootfs"
	@mkdir -p ./plugin/rootfs
	@docker create --name tmp ${PLUGIN_NAME}:rootfs
	@docker export tmp | tar -x -C ./plugin/rootfs
	@echo "### copy config.json to ./plugin/"
	@cp config.json ./plugin/
	@docker rm -vf tmp

create: rootfs
	@echo "### remove existing plugin ${PLUGIN_NAME}:${PLUGIN_TAG} if exists"
	@docker plugin rm -f ${PLUGIN_NAME}:${PLUGIN_TAG} || true
	@echo "### create new plugin ${PLUGIN_NAME}:${PLUGIN_TAG} from ./plugin"
	@docker plugin create ${PLUGIN_NAME}:${PLUGIN_TAG} ./plugin

enable:
	@echo "### enable plugin ${PLUGIN_NAME}:${PLUGIN_TAG}"
	@docker plugin enable ${PLUGIN_NAME}:${PLUGIN_TAG}

enable_debug: disable set_debug enable

set_debug:
	@echo "### set debug mode for ${PLUGIN_NAME}:${PLUGIN_TAG}"
	@docker plugin set ${PLUGIN_NAME}:${PLUGIN_TAG} DEBUG=1

disable:
	@echo "### disable plugin ${PLUGIN_NAME}:${PLUGIN_TAG}"
	-@docker plugin disable ${PLUGIN_NAME}:${PLUGIN_TAG}

push: create enable
	@echo "### push plugin ${PLUGIN_NAME}:${PLUGIN_TAG}"
	@docker plugin push ${PLUGIN_NAME}:${PLUGIN_TAG}

test: create_volume
	-docker run -it --rm --name seafile_busybox -v seafile_volume:/data busybox ls /data

create_volume:
	@echo "### create volume ${PLUGIN_NAME}:${PLUGIN_TAG}"
	@docker volume create -d ${PLUGIN_NAME} -o url=https://seafile.visara.technology/seafdav -o username=theo@minacori.dev -o password=m8PKWNRUSMm4TNSXvkqR4BxfebMtygRo seafile_volume

clean_test:
	@echo "### remove container seafile_busybox"
	-@docker rm seafile_busybox
	@echo "### remove volume seafile_volume"
	-@docker volume rm seafile_volume

# Need jq package
logs:
	tail -f /run/docker/plugins/$(shell docker plugin inspect visaratechnology/docker-volume-davfs | jq '.[]?.Id' | tr -d '"')/init-stderr & \
	tail -f /run/docker/plugins/$(shell docker plugin inspect visaratechnology/docker-volume-davfs | jq '.[]?.Id' | tr -d '"')/init-stdout & \
	tail -f /var/lib/docker/plugins/$(shell docker plugin inspect visaratechnology/docker-volume-davfs | jq '.[]?.Id' | tr -d '"')/rootfs/docker-volume-davfs.log
	
alltest: clean_test disable clean rootfs create enable test logs