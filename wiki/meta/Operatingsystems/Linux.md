# Use Linux!

As a computer scientist your natural first choice is using Linux as your operating system. However, there are some requirements that you will need to solve to use Linux in a productive reasearcher environment.
This section will discuss, what and how you can achieve tasks, which might have been easier solved by using a different OS.

## Email

### Betterbird

## Editor

### VS Code

### NeoVim

#### LunarVim



## Dying by a thousand cuts
As one non-linux user might put it, linux is a hassle. Or it can be, if you don't know how to address arising issues (`#skill-issue`). This section is motivated by the edge-cases which can not be expected to be common knowledge.

### WIFIonICE
In case you are a linux power user and als dabbeling in containers (e.g. docker) and then have also docker installed on your mmachine, you might run into issues with Deutsche Bahn.

The wifi network on Deutsche Bahn's ICE trains "WIFIonICE" provides ip addresses in the docker subnet(`172.17.0.1/16`). In case you want to connect to the wifi on your next journey, but are unable to, changing your docker-subnet cidr might help. To achieve this, follow this short guide:

- ensure no docker containers are currently running
- run `docker network prune` to remove all docker networks
- edit your `/etc/docker/daemon.json` - after editing mine looks like this:
```
{
    "log-driver": "json-file",
    "log-opts": { "max-size": "10m", "max-file": "5" },
    "dns": ["172.19.0.1"],
    "bip": "172.19.0.1/16"
}
```
- restart your docker daemon by running `sudo systemctl restart docker`
- docker has now recreated its networks using the new provided network cidr.
- Hopefully, this was also your issue and your instable wifi connection is "working"
