
#include <List.h>
#include <Locker.h>
#include <Autolock.h>
#include <USBKit.h>
#include <map>
#include "libusbi.h"
#include "haiku_usb_raw.h"

using namespace std;

class USBDevice;
class USBDeviceHandle;
class USBTransfer;

class USBDevice {
public:
						USBDevice(const char *);
	virtual					~USBDevice();
	const char*				Location() const;
	uint8					CountConfigurations() const;
	const usb_device_descriptor*		Descriptor() const;
	const usb_configuration_descriptor*	ConfigurationDescriptor(uint32) const;
	const usb_configuration_descriptor*	ActiveConfiguration() const;
	uint8					EndpointToIndex(uint8) const;
	uint8					EndpointToInterface(uint8) const;
	int					ClaimInterface(int);
	int					ReleaseInterface(int);
	int					CheckInterfacesFree(int);
	int					SetActiveConfiguration(int);
	int					ActiveConfigurationIndex() const;
	bool					InitCheck();
private:
	int					Initialise();
	unsigned int				fClaimedInterfaces;	
	usb_device_descriptor			fDeviceDescriptor;
	unsigned char**				fConfigurationDescriptors;
	int					fActiveConfiguration;
	char*					fPath;
	map<uint8,uint8>			fConfigToIndex;
	map<uint8,uint8>*			fEndpointToIndex;
	map<uint8,uint8>*			fEndpointToInterface;
	bool					fInitCheck;
};

class USBDeviceHandle {
public:
				USBDeviceHandle(USBDevice *dev);
	virtual			~USBDeviceHandle();
	int			ClaimInterface(int);
	int			ReleaseInterface(int);
	int			SetConfiguration(int);
	int			SetAltSetting(int, int);
	status_t		SubmitTransfer(struct usbi_transfer *);
	status_t		CancelTransfer(USBTransfer *);
	bool			InitCheck();
private:
	int			fRawFD;
	static status_t		TransfersThread(void *);
	void			TransfersWorker();
	USBDevice*		fUSBDevice;
	unsigned int		fClaimedInterfaces;
	BList			fTransfers;
	BLocker			fTransfersLock;
	sem_id			fTransfersSem;
	thread_id		fTransfersThread;
	bool			fInitCheck;
};

class USBTransfer {
public:
					USBTransfer(struct usbi_transfer *, USBDevice *);
	virtual				~USBTransfer();
	void				Do(int);
	struct usbi_transfer*		UsbiTransfer();
	void				SetCancelled();
	bool				IsCancelled();
private:
	struct usbi_transfer*		fUsbiTransfer;
	struct libusb_transfer*		fLibusbTransfer;
	USBDevice*			fUSBDevice;
	BLocker				fStatusLock;
	bool				fCancelled;
};

class USBRoster {
public:
			USBRoster();
	virtual		~USBRoster();
	int		Start();
	void		Stop();
private:
	void*		fLooper;
};
