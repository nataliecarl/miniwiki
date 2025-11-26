# Printers
Kyocera TASKalfa 352ci color multifunction printer. Supports duplex color printing via KPDL driver. Connected through 130.149.253.141 (e.g. socket://130.149.253.141). Uses ISO A4 media by default with duplex set to two-sided-long-edge. Scan and email capabilities are available.

|-------------|-----------------------------------------------|
| Description | Kyocera TASKalfa 352ci color printer          |
| Location    | E-N 160 (Archive)                             |
| Driver      | TASKalfa 352ci KPDL (color, 2-sided printing) |
| Connection  | 130.149.253.141 (e.g. socket://)              |
| Defaults    | job-sheets=none, none                         |
| Media       | iso_a4_210x297mm                              |
| Sides       | two-sided-long-edge                           |

There is also a CUPS server running in our openstack cluster, allowing users while connected to the OpenVPN to print using the address ´ipp://10.122.0.52/printers/Kyocera_352ci´. 

## Important
Do not try to print anything with a plot that uses matplotlib's fill_between. This will crash the printer (maybe). Don't ask me how I know.

## Hinweise für Computer-Nutzende mit besonderen Bedürfnissen (MacOS)
MacOS macht manchmal Sachen, manchmal aber auch nicht so wie man sich das so wünschen würde. Deshalb sind hier verschiedene Lösungsansätze für den Fall, dass das teure Ding mal wieder die Arbeit verweigert.

1. Drucker in den Einstellungen löschen und neu hinzufügen. Klappt manchmal.
2. Über's Terminal drucken klappt auch manchmal, da ist es nur nervig, alles richtig einzustellen. Funktioniert im Prinzip aber z. B. wie folgt. ```lpr -P Drucker main.pdf```
3. VirtualBox mit Ubuntu o. Ä. laufen lassen und daraus drucken. Scheinbar kann Linux das besser, selbst wenn es virtuell auf einem Mac läuft. Damit das klappt, sind 2 Dinge wichtig: (i) Die VirtualBox Guest Tools installieren, das findet man unter Devices -> Insert Guest Additions CD Image. Dann kann man das in Ubuntu öffnen und das darin enthaltene Skript ausführen. (ii) Jetzt können wir unser home-Verzeichnis in die VM mounten. Das geht über das Menü zur VM in VirtualBox, da muss man dann aber selber die Stelle zum mounten angeben, sonst bekommt man mit den Permissions Probleme. 
