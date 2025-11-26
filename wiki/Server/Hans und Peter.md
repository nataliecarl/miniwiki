# Dell PowerEdge R450 Servers

Here is everything we know and will set up.

## Hardware

We have two Dell PowerEdge R450 servers.
The product code is [PER4509A](https://www.klarsicht-it.de/DELL-PowerEdge-R450-2x-Xeon-Silver-4310/26029580).

They have two Xeon Silver 4310 each, which are 12 core chips.
They have 64GB of RAM each (four 16GB DIMMs).
They each have two SAS 600GB HDDs (10000rpm), with six more hard drive slots available each.
They have a PERC H755 Front raid controller.
As far as we know, they have a 3-year next business day extended warranty, starting at our delivery date of 20th February 2024.

## Configuration

The servers are currently installed in a desktop rack in EN176.
During installation, one of the rack rails got a bit damaged, but it appears to be fine.

The hard drives are each configured in RAID1.

The names of the servers are:

* Peter (upper)
* Hans (lower)

## SSDs

We added two Samsung 960GB SAS SSDs to each server.
The model is: <https://direkt.jacob.de/produkte/samsung-960gb-ssd-mzilt960hbhq-00007-artnr-6139659.html>

For each server, in iDRAC, we added a virtual disk with RAID0 (we need speed, not fault tolerance).

## Networking

Here are the network addresses of the machines.
We only use the 1G Ethernet ports, not the SFP+ ports.
We don't have that kind of money.

### Peter

LAN1: a8:3c:a5:01:64:1e
LAN2: a9:3c:a5:01:64:1f
LAN3 (SFP+ 1): 04:32:01:1e:1b:b0
iDRAC: a8:3c:a5:01:64:18
iDRAC hostname: srv-peter-idrac

#### DHCP

IP LAN1: 130.149.253.149/26
LAN1 hostname: server-peter-lan1
IP LAN3: 10.35.0.2/16
Gateway: 10.35.0.1 (Hans)
IP iDRAC: 130.149.253.151

### Hans

LAN1: a8:3c:a5:01:83:6e
LAN2: a8:3c:a5:01:83:6f
LAN3 (SFP+ 1): 04:32:01:1e:00:e0
iDRAC: a8:3c:a5:01:83:68
iDRAC hostname: srv-hans-idrac

#### DHCP

IP LAN1: 130.149.253.144/26
LAN1 hostname: server-hans-lan1
IP LAN3: 10.35.0.1/16
Gateway: 130.149.253.129
IP iDRAC: 130.149.253.146

### Private Network

In order to make the two servers talk to each other properly, we have also set up a private LAN.
We simply connect LAN3 (SFP+ P1) of Hans and LAN2 of Peter to each other.
We have them configured in a 10.35.0.1/16 subnet with static IP addresses.
We have Hans as the main router, which also does NAT.
Peter gets all external traffic via Hans.

## Dell Digital Locker

For some reason, we have our licenses for iDRAC in a digital locker.
If we ever need it, here are the details:

Link: <https://www.dell.com/software-and-subscriptions/en-us/softwares/Index>
Username: [t.schirmer@tu-berlin.de](mailto:t.schirmer@tu-berlin.de)
Password: 9.QeVRw8WxfYVkVfsJ4*

## iDRAC

iDRAC is the integrated management interface.
It can be used even if the server is turned off.
As long as it is connected to the network.

Simply navigate to the iDRAC IP address and skip the security warning.
Then sign in the with admin passwords:

Username: root
Password (Peter): CpYHsXQ@QC3CrpfCje-!
Password (Hans): YNnYRh*2C!JeKLV9JY.r

For reference, you can find the default passwords on the little slips in the server.

For most stuff, you can use the Dell documentation.
For reference, we have iDRAC 9.

For some simple config, I recommend using the integrated Lifecycle Controller.
You can access it via the virtual console during boot by pressing F10.
You can also use this for software updates and OS installation.

### Drivers and Downloads

<https://www.dell.com/support/home/en-us/product-support/product/poweredge-r450/drivers>

### Updating Firmware

We currently have 1.12.1, which is up-to-date as of February 2024.
<https://www.dell.com/support/kbdoc/en-us/000134013/dell-poweredge-update-the-firmware-of-single-system-components-remotely-using-the-idrac>

### Installing an OS

<https://www.dell.com/support/kbdoc/en-us/000130160/how-to-install-the-operating-system-on-a-dell-poweredge-server-os-deployment#1>.

<https://www.dell.com/support/kbdoc/en-us/000124001/using-the-virtual-media-function-on-idrac-6-7-8-and-9>

We recommend using the Lifecycle Controller for installation.
Mount your ISO file as virtual media, boot the server, then start the Lifecycle Controller with F10.
You can do all of this from the comfort of your own desk using iDRAC.
Simply select the "Configure RAID and Deploy an Operating System" option.

## Proxmox

We use Proxmox Virtual Environment 8.1.

### Root Password

Username: root
Password (Peter): 9u8dBQomJdyyj6yU@xm.
Password (Hans): wmyxpx4FeY_9-9F3yTZb

Log in on the Proxmox web interface:

Peter: <https://130.149.253.149:8006/>
Hans: <https://130.149.253.144:8006/>

### Configuration

We have the default Proxmox VE 8.1 configuration.
We only have minor changes to make our two nodes a cluster (yay cluster!).
Here are the docs: <https://pve.proxmox.com/pve-docs/chapter-pvecm.html>

Our cluster is called: 3s-cluster
We created the cluster on Hans and joined it on Peter.
We need to enter the peer root password to join.

## Repositories

As we don't want to pay for Proxmox because we're cheap, we have to use the no-subscription repositories.
These are not the default.
In the web interface, go to each server and click Updates > Repositories.
Disable `https://enterprise.proxmox.com/debian/pve` and `https://enterprise.proxmox.com/debian/ceph-quincy/`.
Click Add and add the `No-subscription` repository.

### Network Config

We have a single public (and private) IP for each of our servers.
Normally, VMs would use DHCP to also get IP addresses.
But we only have one per server.
So we have to set up NAT.

What will happen is this:
Each server keeps their WAN/TUB IP address.
Each VM has an IP in a private network.
Only Hans will be the router for our private network.
However, we still need Proxmox on Peter available via the TUB address.
We follow this documentation: <https://pve.proxmox.com/pve-docs/chapter-sysadmin.html#sysadmin_network_masquerading>
Further, we also follow this much better documentation: <https://www.linode.com/docs/guides/linux-router-and-ip-forwarding/?tabs=iptables>

This is the `/etc/network/interfaces` of Hans:

```sh
auto lo
iface lo inet loopback

auto eno8303
iface eno8303 inet static
        # WAN IP of Hans
        address 130.149.253.144/26
        # TUB gateway
        gateway 130.149.253.129

auto vmbr0
iface vmbr0 inet static
        # bridge this bridge with private interface
        bridge-ports eno12399np0
        bridge-stp off
        bridge-fd 0
        # private network address
        address 10.35.0.1/16

auto eno12399np0
iface eno12399np0 inet static
        # allow IPv4 forwarding
        post-up echo 1 > /proc/sys/net/ipv4/ip_forward
        # allow IPv6 forwarding
        post-up echo 1 > /proc/sys/net/ipv6/conf/all/forwarding
        # allow ARP proxying just in case
        post-up echo 1 > /proc/sys/net/ipv4/conf/eno8303/proxy_arp
        # I don't know what this does but it's important
        post-up iptables -A FORWARD -j ACCEPT
        # enable NAT
        post-up iptables -t nat -s 10.35.0.0/16 -A POSTROUTING -j MASQUERADE

# unused Ethernet interface
iface eno8403 inet manual
# unused SFP+ interface
iface eno12409np1 inet manual
source /etc/network/interfaces.d/*
```

This is the simpler config for Peter:

```sh
auto lo
iface lo inet loopback

auto eno8303
iface eno8303 inet static
        # we don't set a gateway here
        # we only want this to be available in the private TUB network
        address 130.149.253.149/26

auto vmbr0
iface vmbr0 inet static
        # bridge the private network interface
        bridge-ports eno12399np0
        bridge-stp off
        bridge-fd 0
        # a second private IP
        address 10.35.0.2/16
        # the gateway is Hans
        gateway 10.35.0.1

auto eno12399np0
# no settings necessary here
iface eno12399np0 inet static

# unused Ethernet interface
iface eno8403 inet manual
# unused SFP+ interface
iface eno12409np1 inet manual
source /etc/network/interfaces.d/*
```

Any changes to `/etc/network/interfaces` can be realized using `ifdown -a ; ip addr flush eno8303 ; ip addr flush eno12399np0 ; ip addr flush eno8403 ; ifup -a`.
Make sure to put this as one command as your network connection will be down while it's running!

We also need to move our cluster to our dedicated network.
This is something that's very easy to do during initialization, but I forgot.
Alas, on the main node (Hans), but you need to do this on every node:

```sh
cp /etc/pve/corosync.conf /etc/pve/corosync.conf.new
cp /etc/pve/corosync.conf /etc/pve/corosync.conf.bak
nano /etc/pve/corosync.conf.new
```

Replace the `ring0_addr` of Hans with 10.35.0.1 and of Peter with 10.35.0.2.
Increment the `config_version` by one!
Otherwise, Proxmox may complain.
Then execute:

```sh
mv /etc/pve/corosync.conf.new /etc/pve/corosync.conf
```

You can check if everything went well with:

```sh
systemctl status corosync
journalctl -b -u corosync
```

You may need to restart Corosync:

```sh
systemctl restart corosync
```

You can check that the local network works with `iperf`:

```sh
sudo apt-get update
sudo apt-get install iperf3
```

Peter: `iperf3 -s`
Hans: `iperf3 -c 10.35.0.2`
Output:

```text
-----------------------------------------------------------
Server listening on 5201 (test #1)
-----------------------------------------------------------
Accepted connection from 10.35.0.1, port 38046
[  5] local 10.35.0.2 port 5201 connected to 10.35.0.1 port 38056
[ ID] Interval           Transfer     Bitrate
[  5]   0.00-1.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   1.00-2.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   2.00-3.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   3.00-4.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   4.00-5.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   5.00-6.00   sec  1.10 GBytes  9.42 Gbits/sec
[  5]   6.00-7.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   7.00-8.00   sec  1.10 GBytes  9.42 Gbits/sec
[  5]   8.00-9.00   sec  1.10 GBytes  9.41 Gbits/sec
[  5]   9.00-10.00  sec  1.10 GBytes  9.42 Gbits/sec
[  5]  10.00-10.00  sec   144 KBytes  7.77 Gbits/sec
- - - - - - - - - - - - - - - - - - - - - - - - -
[ ID] Interval           Transfer     Bitrate
[  5]   0.00-10.00  sec  11.0 GBytes  9.41 Gbits/sec                  receiver
-----------------------------------------------------------
Server listening on 5201 (test #2)
-----------------------------------------------------------
```

#### Port Forwarding

Pretty easy to do! To forward a port from the outside to one of the VMs, edit `/etc/networking/interfaces`. Adding `post-up iptables -t nat -A PREROUTING -p tcp --dport 50500 -j DNAT --to-destination 10.35.15.51:5000` as option to `iface eno8303 inet static` forwards the outside port 50500 of the server to `10.35.15.51:5000`, which just happens to be one of our VMs.

### Storage Config

We have our basic HDD RAID0 for the general stuff, because it has fault tolerance but does not need to be that fast.
It's where our Proxmox lives.
However, we also want to use the SSDs to host our VMs, because they are so fast (but can fail).
To make that happen, we do the following (on each server):

````
1. Format the disk

```sh
# list the block devices
$ lsblk
NAME                            MAJ:MIN RM   SIZE RO TYPE MOUNTPOINTS0
sda                               8:0    0   1.7T  0 disk
# there is a device called sda that is our SSD
# let's format that with ext4
$ sudo mkfs -t ext4 /dev/sda
$ lsblk -f
NAME                            FSTYPE      FSVER    LABEL  UUID                                   FSAVAIL FSUSE% MOUNTPOINTS
sda                             ext4        1.0             aa482432-cb13-47ad-b2f1-88719a52f62a
# we now have ext4
```

1. Create a mount point (named group1 like our virtual disk)

```sh
sudo mkdir -p /group1
sudo mount -t auto /dev/sda /group1
```

1. Automount the drive by adding this line to `/etc/fstab`:

```text
/dev/sda /group1 ext4 errors=remount-ro 0 1
```

1. In Proxmox GUI, go to "Datacenter", select "Storage", click "Add", click "Directory", point to `/group1`, give name `ssd`, select all, enable.
````

We can now move all of our disk images to `ssd`.
We also disable the default `local` storage backend.
Make sure to select this as standard for new VMs.

Is the `directory` type the best idea? Probably not.
See <https://pve.proxmox.com/wiki/Storage>

### Basic Usage

Note that all nodes in the cluster must be on to do anything in Proxmox.
All VMs and VM templates must have a unique numerical ID.

### Creating a Cloud Init Template

Cloud-init lets us configure VMs easily.
The recommended way to boot cloud-init VMs on Proxmox is to configure one and then use it as a template.
Here is the documentation: <https://pve.proxmox.com/wiki/Cloud-Init_Support>

Here is an example:

1. Open the shell on a server by clicking the server and selecting "Shell" in the top right
2. Download Ubuntu 24.04 LTS Noble Server. A list of images is at <https://cloud-images.ubuntu.com/>

   ```sh
   wget https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
   ```
3. Create the VM with ID `9001`. This one gets 8GB of memory and 1 vCPUs.

   ```sh
   qm create 9001 --memory 8192 --vcpus 1 --net0 virtio,bridge=vmbr0 --scsihw virtio-scsi-pci
   ```
4. Import the Ubuntu disk image into a new hard disk:

   ```sh
   qm set 9001 --scsi0 local-lvm:0,import-from=/root/noble-server-cloudimg-amd64.img
   ```

   Note that the disk is now always full.
   You must resize it to do anything meaningful.
   However, it's smart to do the resizing after creating a VM from this template, as that will reduce the migration and copying time.
5. Then we need to add the `cloud-init` CD-ROM drive:

   ```sh
   qm set 9001 --ide2 local-lvm:cloudinit
   # make sure to still boot from the hard drive
   qm set 9001 --boot order=scsi0
   ```
6. You can already add some `cloud-init` configuration for the template:

   ```sh
   USER=tobias
   qm set 9001 --ciuser $USER \
       --sshkey /home/$USER/.ssh/id_rsa.pub \
       --cipassword changeme
   qm set 9001 --name $USER-ubuntu-template
   qm set 9001 --ipconfig0 ip=10.35.0.3/16,gw=10.35.0.1
   ```
7. Finally, we convert this VM to a template.

   ```sh
   qm template 9001
   ```

### Starting a VM

Right-click the template and click "clone".
Set a new ID.
In `cloud init` go to network and give a unique IP address in the `10.35.0.0/16` network (above 10.35.0.2, please!)
Set `10.35.0.1` as the gateway.
Click boot and enjoy!

### Users and SSH

I recommend creating a custom user account for SSH.
In the web interface, open a shell and add a user:

```sh
adduser tobias
```

Set any password you want: this will also be the password you can use to log in that user on the web interface.

Add your public SSH key to `/home/USER/.ssh/id_rsa.pub` and `/home/USER/.ssh/authorized_keys`.

Make sure this public SSH key is also configured in `cloud-init` for your VMs (see the VM template section).

If you create a VM with the IP address `10.35.0.4` and have `cloud-init` configured correctly, you can SSH into it using the Hans jump host:

```sh
ssh -J 130.149.253.144 tobias@10.35.0.4
```

Note that it does not matter where your VM is running, as Hans is always our NAT server.

### SSH Config

I recommend adding this to your `.ssh/config` (replace your private key):

```config
Host hans 130.149.253.144
  IdentityFile ~/.ssh/id_ed25519

Host peter 130.149.253.149
  IdentityFile ~/.ssh/id_ed25519

Host 10.35.*.*
  AddKeysToAgent yes
  IdentityFile ~/.ssh/id_ed25519
  ProxyJump hans
```

You can now simply execute `ssh USER@10.35.0.24` (or whatever IP).
You may need to move any `Host *` entry to the end of your configuration file, [as it may override later pattern matching rules](https://superuser.com/a/1795137).