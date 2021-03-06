package main

import (
	"bytes"
	"code.google.com/p/go.crypto/ssh"
	"flag"
	"fmt"
	"launchpad.net/goamz/aws"
	"launchpad.net/goamz/ec2"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Command Line Variables
var command string

// Flags Variables
var flagSet *flag.FlagSet

// Creation options
var instanceCount int
var instanceImage, instanceType, instanceRegionName, instanceLogin, instanceKey string

// Attack options
var numberOfRequests, concurrentRequests int
var options, url string

// EC2 Variables
var instanceRegion aws.Region

func init() {
	if len(os.Args) == 1 {
		command = "help"
	} else {
		command = os.Args[1]
	}

	flagSet = flag.NewFlagSet(command, flag.ExitOnError)

	// Creation options
	flagSet.IntVar(&instanceCount, "count", 5, "The number of gophers to call into action")
	flagSet.StringVar(&instanceRegionName, "region", "us-east-1", "The availability zone to start each gopher in (default: us-east-1d)")
	flagSet.StringVar(&instanceImage, "image", "ami-2bc99d42", "The image id to use for each gopher (default: ami-2bc99d42)")
	flagSet.StringVar(&instanceType, "type", "t1.micro", "The instance type to use for each gopher (default: t1.micro)")
	flagSet.StringVar(&instanceLogin, "login", "ubuntu", "The ssh username name to use to connect to the new servers (default: ubuntu).")
	flagSet.StringVar(&instanceKey, "key", "gophers", "The ssh key pair name to use to connect to the new gophers (default: gophers)")

	// Attack options
	flagSet.IntVar(&numberOfRequests, "requests", 5, "The number of requests to perform per gopher")
	flagSet.IntVar(&concurrentRequests, "concurrent", 1, "The number of concurrent requests to make per gopher")
	flagSet.StringVar(&url, "url", "", "The url to point the gophers towards when attacking")
	flagSet.StringVar(&options, "options", "", "Additional attack options (Not supported yet)")

	setupRegion()
}

func main() {
	if len(os.Args) <= 1 {
		flagSet.PrintDefaults()
	} else {
		flagSet.Parse(os.Args[2:])

	}

	switch command {
	case "help":
		printInstructions()
	case "up":
		up()
	case "down":
		down()
	case "attack":
		attack()
	case "report":
		report()
	default:
		flagSet.PrintDefaults()
	}
}

func up() {
	fmt.Printf("Adding %v gopher(s) to your army.\n", instanceCount)

	if instanceCount <= 0 {
		fmt.Printf("You must declare a number of gophers greater than 0.")
		return
	}
	createInstances := ec2.RunInstances{
		MinCount:     instanceCount,
		MaxCount:     instanceCount,
		ImageId:      instanceImage,
		InstanceType: instanceType,
		KeyName:      instanceKey,
	}

	ec2Connection, err := ec2Connect()
	runInstancesResponse, err := ec2Connection.RunInstances(&createInstances)
	handleError(err)

	_, err = tagInstances(runInstancesResponse.Instances)
	handleError(err)

	instances, err := findInstances(0)
	handleError(err)

	notReadyInstancesCount := len(instances)
	for notReadyInstancesCount != 0 {
		time.Sleep(3 * time.Second)
		instances, err := findInstances(0)
		handleError(err)

		readyInstancesCount := notReadyInstancesCount - len(instances)

		for i := 0; i < readyInstancesCount; i++ {
			fmt.Printf(".")
		}

		notReadyInstancesCount = len(instances)
	}

	instances, err = findInstances(16)
	handleError(err)

	fmt.Println("\nArming your gophers with grenades, stand back!")

	setupResponseChannel := make(chan setupResponse)
	for _, instance := range instances {
		setupInstance(setupResponseChannel, instance.DNSName)
	}

	for i := 0; i < len(instances); i++ {
		resp := <-setupResponseChannel
		if resp.err != nil {
			fmt.Println("Looks like the gophers were inbred, check your AWS account to make sure things haven't gone awry.")
			fmt.Printf("Message: (%v) %v\n", resp.host, resp.message)
			fmt.Printf("Error: (%v) %v\n", resp.host, resp.err)
		}
	}

	fmt.Printf("\n%v gophers are ready to invade!\n", len(instances))
}

func down() {
	fmt.Println("Your gopher army has been nuked from orbit.")

	instances, err := findInstances(-1)
	handleError(err)

	instanceIds := getInstanceIds(instances)

	ec2Connection, err := ec2Connect()
	ec2Connection.TerminateInstances(instanceIds)
}

func attack() {
	fmt.Println("your gophers are on the move!")

	if url == "" {
		fmt.Println("You must provide a url to start the attack.")
		return
	}

	instances, err := findInstances(16)
	handleError(err)

	attackResponseChannel := make(chan benchmarkResponse)
	for _, instance := range instances {
		startAttack(attackResponseChannel, instance.DNSName)
	}

	var complete, failed int
	var totalTime, requestsPerSecond float32
	for i := 0; i < len(instances); i++ {
		resp := <-attackResponseChannel
		if resp.err != nil {
			fmt.Println("Looks like the gophers started a civil war.")
		}

		complete += resp.complete
		failed += resp.failed
		requestsPerSecond += resp.requestsPerSecond
		totalTime += resp.timePerRequest * float32(resp.complete+resp.failed)
	}

	fmt.Println("Completed requests:", complete)
	fmt.Println("Failed requests:", failed)
	fmt.Println("Requests per second:", requestsPerSecond, "[#/sec] (mean)")
	fmt.Println("Time per request:", totalTime/float32(complete+failed), "[ms] (mean)")
}

func report() {
	fmt.Println("Who know where the gophers are?")

	instances, err := findInstances(-1)
	handleError(err)

	if len(instances) > 0 {
		fmt.Println("Name (DNS Name) - State")
		for _, instance := range instances {
			var instanceName string

			for _, tag := range instance.Tags {
				if tag.Key == "Name" {
					instanceName = tag.Value
				}
			}

			fmt.Printf("%s (%s) - %s\n", instanceName, instance.DNSName, instance.State.Name)
		}
	} else {
		fmt.Println("Your army has gone AWOL.  Better recruit some more!")
	}
}

func setupInstance(response chan setupResponse, host string) {
	go func() {
		setupResponse := setupResponse{host: host}

		var client *ssh.ClientConn
		for i := 0; client == nil || i == 10; i++ {
			time.Sleep(2 * time.Second)
			// fmt.Printf("Attempting to create and ssh connection to %s.\n", host)
			client, _ = sshClient(fmt.Sprintf("%s:22", host))
		}

		var session *ssh.Session
		for i := 0; session == nil || i == 10; i++ {
			time.Sleep(2 * time.Second)
			// fmt.Printf("creating the session: %v\n", client)
			session, _ = client.NewSession()
		}

		// fmt.Println("defering the session")
		defer session.Close()

		// fmt.Println("setting up the buffer")
		var outputBuffer bytes.Buffer
		session.Stdout = &outputBuffer
		// fmt.Println("making the call")
		err := session.Run("sudo apt-get install apache2-utils -y")
		if err != nil {
			setupResponse.err = append(setupResponse.err, err)
		}

		setupResponse.message = outputBuffer.String()
		// fmt.Println("returning the response")
		response <- setupResponse
	}()
}

func startAttack(response chan benchmarkResponse, host string) {
	go func() {
		benchmarkResponse := benchmarkResponse{}

		client, err := sshClient(fmt.Sprintf("%s:22", host))
		if err != nil {
			benchmarkResponse.err = append(benchmarkResponse.err, err)
		}

		session, err := client.NewSession()
		if err != nil {
			benchmarkResponse.err = append(benchmarkResponse.err, err)
		}

		defer session.Close()

		var outputBuffer bytes.Buffer
		session.Stdout = &outputBuffer

		benchmarkCommand := fmt.Sprintf("ab -r -n %v -c %v %v \"%v\"", numberOfRequests, concurrentRequests, options, url)
		err = session.Run(benchmarkCommand)
		if err != nil {
			benchmarkResponse.err = append(benchmarkResponse.err, err)
		}

		// fmt.Println(&outputBuffer)
		outputString := outputBuffer.String()

		for _, line := range strings.Split(outputString, "\n") {
			if strings.Contains(line, "Complete requests:") {
				value := strings.TrimSpace(strings.Split(line, ":")[1])
				benchmarkResponse.complete, _ = strconv.Atoi(value)
			}
			if strings.Contains(line, "Failed requests:") {
				value := strings.TrimSpace(strings.Split(line, ":")[1])
				benchmarkResponse.failed, _ = strconv.Atoi(value)
			}
			if strings.Contains(line, "Requests per second:") {
				re := regexp.MustCompile("Requests per second:\\s*(\\d+.\\d+)")
				value := re.FindStringSubmatch(line)[1]
				valueFloat, _ := strconv.ParseFloat(value, 32)
				benchmarkResponse.requestsPerSecond = float32(valueFloat)
			}
			if strings.Contains(line, "Time per request:") && !strings.Contains(line, "across all concurrent requests") {
				re := regexp.MustCompile("Time per request:\\s*(\\d+.\\d+)\\s*\\[ms\\]\\s*\\(mean\\)")
				value := re.FindStringSubmatch(line)[1]
				valueFloat, _ := strconv.ParseFloat(value, 32)
				benchmarkResponse.timePerRequest = float32(valueFloat)
			}
		}

		response <- benchmarkResponse
	}()
}

func printInstructions() {
	fmt.Println(`gophers COMMAND [options]

gophers with grenades

A utility for arming (creating) many gophers (small EC2 instances) to attack
(load test) targets (web applications).

commands:
  up      Start a batch of load testing servers.
  attack  Begin the attack on a specific url.
  down    Shutdown and deactivate the load testing servers.
  report  Report the status of the load testing servers.
    `)
}

func ec2Connect() (*ec2.EC2, error) {
	auth, err := aws.EnvAuth()
	if err != nil {
		return nil, err
	}

	ec2Connection := ec2.New(auth, aws.USEast)

	return ec2Connection, nil
}

func setupRegion() {
	instanceRegion = aws.Regions[instanceRegionName]
}

func getInstanceIds(instances []ec2.Instance) (instanceIds []string) {
	for _, instance := range instances {
		instanceIds = append(instanceIds, instance.InstanceId)
	}

	return instanceIds
}

func tagInstances(instances []ec2.Instance) (responses []*ec2.SimpleResp, err error) {
	instanceIds := getInstanceIds(instances)

	ec2Connection, err := ec2Connect()
	if err != nil {
		return nil, err
	}

	for _, instance := range instances {
		nameTag := ec2.Tag{"Name", instance.InstanceId}
		gopherTag := ec2.Tag{"gopher", "true"}
		tags := []ec2.Tag{nameTag, gopherTag}

		response, err := ec2Connection.CreateTags(instanceIds, tags)
		if err != nil {
			return nil, err
		}

		responses = append(responses, response)
	}

	return responses, nil
}

func findInstances(statusCode int) (instances []ec2.Instance, err error) {
	filter := ec2.NewFilter()
	filter.Add("tag:gopher", "true")

	if statusCode != -1 {
		filter.Add("instance-state-code", strconv.Itoa(statusCode))
	}

	ec2Connection, err := ec2Connect()
	resp, err := ec2Connection.Instances(nil, filter)
	if err != nil {
		return nil, err
	}

	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

func sshClient(host string) (*ssh.ClientConn, error) {
	var auths []ssh.ClientAuth

	keypath := getKeyPath()
	k := &keyring{}
	err := k.loadPEM(keypath)
	if err != nil {
		return nil, err
	}

	auths = append(auths, ssh.ClientAuthKeyring(k))

	config := &ssh.ClientConfig{
		User: "ubuntu",
		Auth: auths,
	}

	client, err := ssh.Dial("tcp", host, config)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func getKeyPath() string {
	user, _ := user.Current()
	homeDirectory := user.HomeDir

	return filepath.Join(homeDirectory, ".ssh", fmt.Sprintf("%s.pem", instanceKey))
}

func handleError(err error) {
	if err != nil {
		panic(err)
	}
}
