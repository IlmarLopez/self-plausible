package lib

import (
	"fmt"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	ec2 "github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	iam "github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

type PlausibleStackProps struct {
	awscdk.StackProps
}

func NewPlausibleStack(scope constructs.Construct, id string, props *PlausibleStackProps) awscdk.Stack {
	stack := awscdk.NewStack(scope, &id, &props.StackProps)
	accountId := stack.Account()
	region := stack.Region()

	vpc := ec2.Vpc_FromLookup(stack, jsii.String("DefaultVPC"), &ec2.VpcLookupOptions{
		IsDefault: jsii.Bool(true),
	})

	sg := ec2.NewSecurityGroup(stack, jsii.String("PlausibleSG"), &ec2.SecurityGroupProps{
		Vpc:               vpc,
		AllowAllOutbound:  jsii.Bool(true),
		SecurityGroupName: jsii.String("PlausibleSG"),
	})

	sg.AddIngressRule(ec2.Peer_AnyIpv4(), ec2.Port_Tcp(jsii.Number(22)), jsii.String("Allow SSH"), nil)
	sg.AddIngressRule(ec2.Peer_AnyIpv4(), ec2.Port_Tcp(jsii.Number(80)), jsii.String("Allow HTTP"), nil)
	sg.AddIngressRule(ec2.Peer_AnyIpv4(), ec2.Port_Tcp(jsii.Number(443)), jsii.String("Allow HTTPS"), nil)

	role := iam.NewRole(stack, jsii.String("InstanceSSMRole"), &iam.RoleProps{
		AssumedBy: iam.NewServicePrincipal(jsii.String("ec2.amazonaws.com"), nil),
		ManagedPolicies: &[]iam.IManagedPolicy{
			iam.ManagedPolicy_FromAwsManagedPolicyName(jsii.String("AmazonSSMManagedInstanceCore")),
		},
	})

	role.AddToPolicy(iam.NewPolicyStatement(&iam.PolicyStatementProps{
		Effect: iam.Effect_ALLOW,
		Actions: &[]*string{
			jsii.String("ssm:GetParameter"),
			jsii.String("ssm:GetParameters"),
		},
		Resources: &[]*string{
			jsii.String(fmt.Sprintf("arn:aws:ssm:%s:%s:parameter/plausible/*", *region, *accountId)),
		},
	}))

	// User Data script
	userData := ec2.NewMultipartUserData(nil)

	// Create UserData for Linux commands
	linuxCommands := ec2.UserData_ForLinux(&ec2.LinuxUserDataOptions{
		Shebang: jsii.String("#!/bin/bash"),
	})

	// Add commands
	linuxCommands.AddCommands(
		jsii.String("sudo apt-get update -y"),
		jsii.String("sudo apt-get install -y docker.io docker-compose git awscli"),
		jsii.String("sudo systemctl enable docker"),
		jsii.String("sudo systemctl start docker"),
		// Get the instance's region
		jsii.String("REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)"),
		// Retrieve parameters and export them as environment variables
		jsii.String("export SECRET_KEY_BASE=$(aws ssm get-parameter --name '/plausible/secret_key_base' --with-decryption --query Parameter.Value --output text --region $REGION)"),
		jsii.String("export POSTGRES_PASSWORD=$(aws ssm get-parameter --name '/plausible/postgres_password' --with-decryption --query Parameter.Value --output text --region $REGION)"),
		jsii.String("export BASE_URL='https://analytics.ilmarlopez.com'"), // Replace with your subdomain
		// Clone your repository
		jsii.String("cd /home/ubuntu"),
		jsii.String("git clone https://github.com/IlmarLopez/plausible-hosting.git"),
		jsii.String("cd plausible-hosting"),
		// Start Docker Compose
		jsii.String("sudo docker-compose up -d"),
		// Install Nginx and Certbot
		jsii.String("sudo apt-get install -y nginx python3-certbot-nginx"),
		jsii.String("sudo systemctl enable nginx"),
		jsii.String("sudo systemctl start nginx"),
		// Configure Nginx
		jsii.String(`
			sudo bash -c 'cat > /etc/nginx/sites-available/plausible <<'EOF'
			server {
					listen 80;
					server_name analytics.ilmarlopez.com;

					location / {
							proxy_pass http://localhost:8000;
							proxy_set_header Host $host;
							proxy_set_header X-Real-IP $remote_addr;
							proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
							proxy_set_header X-Forwarded-Proto $scheme;
					}
			}
			EOF'`),
		jsii.String("sudo ln -s /etc/nginx/sites-available/plausible /etc/nginx/sites-enabled/"),
		jsii.String("sudo rm /etc/nginx/sites-enabled/default"),
		jsii.String("sudo nginx -t"),
		jsii.String("sudo systemctl restart nginx"),
		// Obtain and configure SSL certificate with Certbot
		jsii.String("sudo certbot --nginx -n --agree-tos --email me@ilmarlopez.com -d analytics.ilmarlopez.com --redirect"),
		// Configure automatic certificate renewal
		jsii.String("echo '0 0 * * * root /usr/bin/certbot renew --quiet' | sudo tee /etc/cron.d/certbot-renew"),
	)

	// Create a MultipartBody from linuxCommands
	linuxUserDataPart := ec2.MultipartBody_FromUserData(
		linuxCommands,
		jsii.String("text/x-shellscript; charset=\"utf-8\""),
	)

	// Add the part to MultipartUserData
	userData.AddPart(linuxUserDataPart)

	// Ubuntu AMI using MachineImage_Lookup
	ami := ec2.MachineImage_Lookup(&ec2.LookupMachineImageProps{
		Name:   jsii.String("ubuntu/images/hvm-ssd/ubuntu-focal-20.04-amd64-server-*"),
		Owners: &[]*string{jsii.String("099720109477")}, // Canonical account ID (Ubuntu)
	})

	// EC2 Instance
	instance := ec2.NewInstance(stack, jsii.String("PlausibleInstance"), &ec2.InstanceProps{
		InstanceType:  ec2.InstanceType_Of(ec2.InstanceClass_BURSTABLE3, ec2.InstanceSize_MICRO),
		MachineImage:  ami,
		Vpc:           vpc,
		SecurityGroup: sg,
		KeyPair:       ec2.KeyPair_FromKeyPairName(stack, jsii.String("KeyPairName"), jsii.String("plausible-keypair")),
		Role:          role,
		UserData:      userData,
	})

	// Assign an Elastic IP to the EC2 instance
	eip := ec2.NewCfnEIP(stack, jsii.String("InstanceEIP"), &ec2.CfnEIPProps{
		Domain:     jsii.String("vpc"),
		InstanceId: instance.InstanceId(),
	})

	// Output the public IP address
	awscdk.NewCfnOutput(stack, jsii.String("InstancePublicIP"), &awscdk.CfnOutputProps{
		Value:       eip.Ref(),
		Description: jsii.String("The public IP address of the EC2 instance"),
	})

	return stack
}
